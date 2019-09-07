package main

import "github.com/vsk8s/k8router/cmd/k8router/cmd"

// Main entry point
func main() {
	obj := cmd.K8router{}
	obj.Run()
}
