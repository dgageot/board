package main

import (
	"log"

	"github.com/dgageot/board/pkg/board"
)

func main() {
	if err := board.Run(); err != nil {
		log.Fatal(err)
	}
}
