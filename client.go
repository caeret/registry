package registry

import (
	"fmt"
	"io/ioutil"
	"net/http"
	"regexp"
	"strings"

	jsoniter "github.com/json-iterator/go"

	"github.com/pkg/errors"

	"github.com/inconshreveable/log15"
	"github.com/parnurzeal/gorequest"
)

type Client struct {
	log15.Logger
	url      string
	authURL  string
	username string
	password string
	request  *gorequest.SuperAgent
	tokens   map[string]string
}

func NewClient(url, username, password string, logger log15.Logger) (*Client, error) {
	c := &Client{
		Logger:   logger,
		url:      url,
		username: username,
		password: password,
		request:  gorequest.New().Set("User-Agent", "caeret-registry-client/1.0"),
		tokens:   make(map[string]string),
	}
	resp, _, errs := c.request.Get(c.url + "/v2/").End()
	if len(errs) > 0 {
		return nil, errs[0]
	}
	switch resp.StatusCode {
	case http.StatusOK:
		return c, nil
	case http.StatusUnauthorized:
		auth := resp.Header.Get("WWW-Authenticate")
		if strings.HasPrefix(auth, "Bearer") {
			r, _ := regexp.Compile(`^Bearer realm="(http.+)",service="(.+)"`)
			if m := r.FindStringSubmatch(auth); len(m) > 0 {
				c.authURL = fmt.Sprintf("%s?service=%s", m[1], m[2])
				c.Info("set bearer auth url.", "url", c.authURL)
			} else {
				return nil, errors.New("no auth service")
			}
		} else if strings.HasPrefix(strings.ToLower(auth), "basic") {
			c.request = c.request.SetBasicAuth(c.username, c.password)
			c.Debug("set basic auth.")
		} else {
			return nil, errors.New("no auth service")
		}
	default:
		return nil, fmt.Errorf("invalid resp status: %d", resp.StatusCode)
	}
	return c, nil
}

func (c *Client) QueryRepositories() ([]string, error) {
	resp, err := c.call("/v2/_catalog", "registry:catalog:*", 2)
	if err != nil {
		return nil, err
	}
	b, _ := ioutil.ReadAll(resp.Body)
	var repositories []string
	jsoniter.Get(b, "repositories").ToVal(&repositories)
	return repositories, nil
}

func (c *Client) QueryTags(repo string) ([]string, error) {
	resp, err := c.call(fmt.Sprintf("/v2/%s/tags/list", repo), fmt.Sprintf("repository:%s:*", repo), 2)
	if err != nil {
		return nil, err
	}
	b, _ := ioutil.ReadAll(resp.Body)
	var tags []string
	if n := jsoniter.Get(b, "tags"); n.ValueType() != jsoniter.NilValue {
		jsoniter.Get(b, "tags").ToVal(&tags)
	}
	return tags, nil
}

func (c *Client) TagInfo(repo, tag string) (digist string) {
	scope := fmt.Sprintf("repository:%s:*", repo)
	resp, err := c.call(fmt.Sprintf("/v2/%s/manifests/%s", repo, tag), scope, 2, false)
	if err != nil {
		c.Error("fail to get tag info.", "repo", repo, "tag", tag, "error", err)
		return
	}
	digist = resp.Header.Get("Docker-Content-Digest")
	return
}

func (c *Client) DeleteTag(repo, tag string) {
	scope := fmt.Sprintf("repository:%s:*", repo)
	c.call(fmt.Sprintf("/v2/%s/manifests/%s", repo, tag), scope, 2, true)
}

func (c *Client) Clean(keepTags ...string) error {
	repos, err := c.QueryRepositories()
	if err != nil {
		return err
	}
	m := make(map[string][]struct{ repo, tag string })
	for _, repo := range repos {
		fmt.Println("repo", repo)
		tags, err := c.QueryTags(repo)
		if err != nil {
			c.Logger.Warn("fail to query tags.", "repo", repo)
			continue
		}
		for _, tag := range tags {
			m[c.TagInfo(repo, tag)] = append(m[c.TagInfo(repo, tag)], struct{ repo, tag string }{repo, tag})
		}
	}

	keep := make(map[string]struct{})
	for _, tag := range keepTags {
		keep[tag] = struct{}{}
	}

	for _, v := range m {
		var del = true
		for _, e := range v {
			_, ok := keep[e.tag]
			if ok {
				del = false
			}
		}
		if del {
			c.DeleteTag(v[0].repo, v[0].tag)
		}
	}

	return nil
}

func (c *Client) call(path, scope string, manifest int, delete ...bool) (gorequest.Response, error) {
	request := c.request.Clone().Set("Accept", fmt.Sprintf("application/vnd.docker.distribution.manifest.v%d+json", manifest))
	if c.authURL != "" {
		request.Set("Authorization", fmt.Sprintf("Bearer %s", c.getToken(scope)))
	}
	resp, body, errs := request.Get(c.url + path).End()
	if len(errs) > 0 {
		return nil, errs[0]
	}
	c.Info("call registry.", "path", path, "status", resp.StatusCode)
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("invalid response %d:%s", resp.StatusCode, body)
	}
	if len(delete) > 0 && delete[0] {
		digest := resp.Header.Get("Docker-Content-Digest")
		parts := strings.Split(path, "/manifests/")
		path = parts[0] + "/manifests/" + digest
		resp, _, errs := request.Delete(c.url + path).End()
		if len(errs) > 0 {
			return nil, errs[0]
		} else {
			// Returns 202 on success.
			c.Info("delete tag.", "tag", parts[1], "status", resp.StatusCode)
		}
		return resp, nil

	}
	return resp, nil
}

func (c *Client) getToken(scope string) string {
	if token, ok := c.tokens[scope]; ok {
		resp, _, _ := c.request.Clone().Get(c.url+"/v2/").Set("Authorization", fmt.Sprintf("Bearer %s", token)).End()
		if resp != nil && resp.StatusCode == 200 {
			return token
		}
	}

	resp, data, errs := c.request.Clone().Get(fmt.Sprintf("%s&scope=%s", c.authURL, scope)).SetBasicAuth(c.username, c.password).EndBytes()
	if len(errs) > 0 {
		c.Error("failed to get token.", "error", errs[0])
		return ""
	}
	if resp.StatusCode != 200 {
		c.Error("failed to get token for scope.", "scope", scope, "resp", string(data))
		return ""
	}

	c.tokens[scope] = jsoniter.Get(data, "token").ToString()
	c.Info("received new token for scope.", "scope", scope)
	return c.tokens[scope]
}
