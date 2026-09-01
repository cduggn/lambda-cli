// lam is a small CLI for Lambda Cloud: launch GPU instances with cloud-init bootstrap,
// wait for ssh, list, ssh in, terminate. See README.md.
package main

import (
	"os"

	"github.com/cduggn/lambda-cli/internal/cli"
)

var version = "dev"

func main() { os.Exit(cli.Execute(version)) }
