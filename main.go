package main

import _ "github.com/lib/pq"
import (
	"fmt"
	"os"
	"database/sql"
	"github.com/milan616/gator/internal/config"
	"github.com/milan616/gator/internal/database"
)

func main() {
	// 1. Read the configuration file
	cfg, err := config.Read()
	if err != nil {
		fmt.Printf("Error reading config: %v\n", err)
		os.Exit(1)
	}

	// 2. Initialize the application state
	programState := &state{
		cfg: &cfg,
	}

	db, err := sql.Open("postgres", cfg.DBURL)
	dbQueries := database.New(db)
	programState.db = dbQueries

	// 3. Initialize the commands registry and register 'login'
	cmds := commands{
		handlers: make(map[string]func(*state, command) error),
	}
	cmds.register("login", handlerLogin)
	cmds.register("register", handlerRegister)
	cmds.register("reset", handlerReset)
	cmds.register("users", handlerUsers)
	cmds.register("agg", handlerAgg)
	cmds.register("feeds", handlerListFeeds)

	// Protected commands wrapped with logged-in middleware
	cmds.register("addfeed", middlewareLoggedIn(handlerAddFeed))
	cmds.register("follow", middlewareLoggedIn(handlerFollow))
	cmds.register("following", middlewareLoggedIn(handlerFollowing))
	cmds.register("unfollow", middlewareLoggedIn(handlerUnfollow))
	cmds.register("browse", middlewareLoggedIn(handlerBrowse))


	// 4. Validate command-line arguments length
	if len(os.Args) < 2 {
		fmt.Println("Error: not enough arguments provided")
		os.Exit(1)
	}

	// 5. Parse command-line args into the command struct
	cmdName := os.Args[1]
	cmdArgs := os.Args[2:]

	cmd := command{
		name: cmdName,
		args: cmdArgs,
	}

	// 6. Run the requested command
	err = cmds.run(programState, cmd)
	if err != nil {
		fmt.Printf("Error: %v\n", err)
		os.Exit(1)
	}
}
