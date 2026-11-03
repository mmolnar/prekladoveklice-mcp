// Command pk searches Czech RPG translation keys from prekladoveklice.d20.cz.
package main

import (
	"os"

	"github.com/mmolnar/prekladoveklice-mcp/internal/cli"
)

func main() { os.Exit(cli.Execute()) }
