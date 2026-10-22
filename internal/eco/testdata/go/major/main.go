package main

import (
	"fmt"

	"example.com/lib"
	l3 "example.com/lib/v3"
	"example.com/library"
)

func main() {
	fmt.Println(lib.Hello(), l3.Hello(), library.Name, "example.com/lib")
}
