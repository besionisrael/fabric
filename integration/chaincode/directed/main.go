package main

import (
	"fmt"
	"os"

	"github.com/hyperledger/fabric-chaincode-go/v2/shim"
)

func main() {
	cc := new(DirectedTraceability)
	if err := shim.Start(cc); err != nil {
		fmt.Fprintf(os.Stderr, "chaincode start error: %v\n", err)
		os.Exit(1)
	}
}
