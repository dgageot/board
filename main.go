package main

import (
	"log"
	"os"

	"github.com/dgageot/board/pkg/board"
	"github.com/dgageot/board/pkg/credentials"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "credential-exec" {
		if len(os.Args) < 4 {
			log.Fatal("usage: board credential-exec <socket> <command> [args...]")
		}
		if err := credentials.Exec(os.Args[2], os.Args[3:]); err != nil {
			log.Fatal(err)
		}
		return
	}
	if err := board.Run(); err != nil {
		log.Fatal(err)
	}
}
