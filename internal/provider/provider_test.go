package provider

import (
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
)

// testAccProtoV6ProviderFactories are used to instantiate a provider during
// acceptance testing. The factory function will be invoked for every Terraform
// CLI command executed to create a provider server to which the CLI can
// reattach.
var testAccProtoV6ProviderFactories = map[string]func() (tfprotov6.ProviderServer, error){
	"onepassword": providerserver.NewProtocol6WithError(New("test")()),
}

func testAccProviderConfig(url string) string {
	return fmt.Sprintf(`
data "aws_secretsmanager_secret" "onepassword_token" {
  name = "onepassword-token"
}
data "aws_secretsmanager_secret_version" "onepassword_token" {
  secret_id = data.aws_secretsmanager_secret.onepassword_token.id
}
provider "onepassword" {
  url   = "%s"
  token = data.aws_secretsmanager_secret_version.onepassword_token.secret_string
}`, url)
}
