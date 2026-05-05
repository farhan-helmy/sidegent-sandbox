package main

import (
	"log"
)

func main() {
	if err := runServe(8080, 10, 30, 120); err != nil {
		log.Fatal(err)
	}
}
