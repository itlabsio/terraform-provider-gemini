// Command terraform-provider-gemini — Terraform-провайдер для API инстанса Gemini.
package main

import (
	"context"
	"flag"
	"log"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"

	"github.com/itlabsio/terraform-provider-gemini/internal/provider"
)

// version подставляется при сборке: -ldflags "-X main.version=1.2.3".
var version = "dev"

func main() {
	var debug bool
	flag.BoolVar(&debug, "debug", false, "run the provider with support for debuggers like delve")
	flag.Parse()

	err := providerserver.Serve(context.Background(), provider.New(version), providerserver.ServeOpts{
		Address: "registry.terraform.io/itlabsio/gemini",
		Debug:   debug,
	})
	if err != nil {
		log.Fatal(err)
	}
}
