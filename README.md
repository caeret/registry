# 使用说明

```go
package main

import (
	"github.com/inconshreveable/log15"

	"github.com/caeret/registry"
)

func main() {
	logger := log15.New()
	cli, err := registry.NewClient("https://registry.example.com", "user", "passwd", logger)
	if err != nil {
		logger.Error("fail to create new client.", "error", err)
		return
	}
	err = cli.Clean("master", "develop", "legacy")
	if err != nil {
		logger.Error("fail to clean images.", "error", err)
	}
}
```