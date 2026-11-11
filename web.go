package main

import (
	"embed"
	"io/fs"
)

//go:embed all:frontend/dist
var embedded embed.FS

func frontendFS() fs.FS {
	sub, err := fs.Sub(embedded, "frontend/dist")
	if err != nil {
		panic(err)
	}
	return sub
}
