package main

import (
	"fmt"
	"os"

	"example.com/users/audit"
	"example.com/users/export"
	"example.com/users/service"
	"example.com/users/store"
)

func main() {
	if len(os.Args) > 1 {
		u, err := store.LoadUser(os.Args[1])
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		fmt.Println(audit.Line("show", u))
		g, _ := service.Greet(u.ID)
		fmt.Println(g)
		return
	}
	fmt.Print(export.CSV(store.ListUsers()))
}
