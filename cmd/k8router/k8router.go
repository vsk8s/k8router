package main

import "github.com/soseth/k8router/cmd/k8router/cmd"

// Main entry point
func main() {
	obj := cmd.K8router{}
	obj.Run()
}
