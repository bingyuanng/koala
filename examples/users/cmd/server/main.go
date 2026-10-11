// The foundation fixture compiles against checked-in transport artifacts.
// Phase 3 adds Koala runtime startup and both listeners to this entry point.
package main

import (
	"fmt"

	"github.com/bingyuanng/koala/config"
	"github.com/bingyuanng/koala/examples/users/internal/deps"
	"github.com/bingyuanng/koala/examples/users/internal/service/user"
)

func main() {
	cfg := config.DefaultConfig()
	cfg.App.Name = "users"
	if _, err := user.New(deps.New()); err != nil {
		panic(err)
	}
	fmt.Printf("%s foundation fixture; runtime startup is planned for Phase 3\n", cfg.App.Name)
}
