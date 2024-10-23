package provider

import (
	"encoding/base64"
	"errors"
	"fmt"
	"github.com/1Password/connect-sdk-go/connect"
	"github.com/1Password/connect-sdk-go/onepassword"
	"github.com/avast/retry-go/v4"
	"github.com/davecgh/go-spew/spew"
	"github.com/gruntwork-io/terratest/modules/aws"
	"github.com/gruntwork-io/terratest/modules/docker"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/thanhpk/randstr"
	"io/ioutil"
	"os"
	"strings"
	"testing"
	"text/template"
)

const (
	target_vault = "Shared-lynqtech-playground"
)

func test1PasswordConnection(connectUrl, token, vault string) bool {
	client := connect.NewClient(connectUrl, token)
	v, err := client.GetVaultByTitle(vault)
	fmt.Println(v)
	if err != nil {
		return false
	}
	return true
}

func TestItemResourceIntegrationUsername(t *testing.T) {

	token, err := aws.GetSecretValueE(t, "eu-central-1", "onepassword-token")
	if err != nil {
		panic(err)
	}
	creds, err := aws.GetSecretValueE(t, "eu-central-1", "onepassword-credentials-file")
	if err != nil {
		panic(err)
	}
	// credentials file is base64 encoded -> decode it here to be stored as plain file later on
	rawCreds, err := base64.RawStdEncoding.DecodeString(creds)
	if err != nil {
		panic(err)
	}

	// create tempdir for all our files
	tempDir, err := ioutil.TempDir("", "TestPasswordGetItems")
	if err != nil {
		panic(err)
	}
	defer func(path string) {
		err := os.RemoveAll(path)
		if err != nil {
			panic(err)
		}
	}(tempDir)

	// write a credentials.json config file
	credentialsJson, err := os.CreateTemp(tempDir, "credential_json")
	if err != nil {
		panic(err)
	}
	defer func(name string) {
		err := os.Remove(name)
		if err != nil {
			panic(err)
		}
	}(credentialsJson.Name())
	if err = os.WriteFile(credentialsJson.Name(), rawCreds, 0600); err != nil {
		panic(err)
	}

	// generate docker compose config from template to mount the credentials file
	tpl, err := template.ParseFiles("docker-compose.yaml")
	if err != nil {
		panic(err)
	}
	// we can't use temporary file here as docker-compose.yaml naming is fixed to be found by stupid docker
	dockerComposeFile, _ := os.Create(fmt.Sprintf("%s/docker-compose.yaml", tempDir))
	defer func(dockerComposeFile *os.File) {
		err := dockerComposeFile.Close()
		if err != nil {
			panic(err)
		}
	}(dockerComposeFile)
	if err := tpl.Execute(dockerComposeFile, map[string]string{
		"onepassword_credentials": credentialsJson.Name(),
	}); err != nil {
		panic(err)
	}

	dockerOptions := &docker.Options{
		// Directory where docker-compose.yml lives
		WorkingDir:     tempDir,
		EnableBuildKit: true,
	}
	defer docker.RunDockerCompose(t, dockerOptions, "down", "--remove-orphans")

	err = retry.Do(
		func() error {
			docker.RunDockerCompose(t, dockerOptions, "build", "--no-cache")
			docker.RunDockerCompose(t, dockerOptions, "up", "-d")

			ok := test1PasswordConnection("http://localhost:8080", token, target_vault)
			if !ok {
				docker.RunDockerCompose(t, dockerOptions, "down", "--remove-orphans")
				return errors.New("1Password test connection failed")
			}
			return nil
		},
	)
	if err != nil {
		t.Error(err)
	}

	title := fmt.Sprintf("TestItemResourceIntegrationUsername-%s", randstr.String(6))
	// general `login` item that doesn't have username set at all
	expectedItem := &onepassword.Item{
		Title:    title,
		Category: onepassword.Login,
	}

	// general `login` item with username set in Terraform
	expectedItemUpdate := generateLoginItem()
	expectedItemUpdate.Title = title

	// general `login` item with username set to "" in order to reset the username value
	expectedItemUpdateReset := generateLoginItem()
	expectedItemUpdateReset.Title = title
	unField := getFieldByName(expectedItemUpdateReset, "username")
	unField.Value = ""
	expectedItemUpdateReset.URLs = []onepassword.ItemURL{
		{
			Primary: true,
			URL:     "",
		},
	}

	manuallySetUrl := "manually-set.net"
	manuallySetUsername := "manually set"

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		ExternalProviders: map[string]resource.ExternalProvider{
			"aws": {
				VersionConstraint: "~> 5.0",
				Source:            "hashicorp/aws",
			},
		},
		Steps: []resource.TestStep{
			{
				PreConfig: func() {
					fmt.Println("1. step\nTerraform code:")
					fmt.Println(integrationLoginProviderConfig("http://localhost:8080") + integrationLoginResourceConfig(expectedItem))
				},
				Config: integrationLoginProviderConfig("http://localhost:8080" /*testServer.URL*/) + integrationLoginResourceConfig(expectedItem),
				Check: resource.ComposeAggregateTestCheckFunc(
					// verify local values
					resource.TestCheckResourceAttr("onepassword_item.test-database", "title", expectedItem.Title),
					resource.TestCheckResourceAttr("onepassword_item.test-database", "category", strings.ToLower(string(expectedItem.Category))),
					resource.TestCheckResourceAttr("onepassword_item.test-database", "username", ""),
					resource.TestCheckResourceAttr("onepassword_item.test-database", "url", ""),
					resource.TestCheckResourceAttrSet("onepassword_item.test-database", "password"),
				),
			},
			{
				PreConfig: func() {
					fmt.Println("2. step\noverride the username manually")

					item, err := getItemByName("http://localhost:8080", token, target_vault, title)
					if err != nil || item == nil {
						t.Error(err)
					}
					f := getFieldByName(item, "username")
					f.Value = manuallySetUsername
					item.URLs = []onepassword.ItemURL{
						{
							Primary: true,
							URL:     manuallySetUrl,
						},
					}

					item, err = setItem("http://localhost:8080", token, target_vault, item)
					if err != nil {
						t.Error(err)
					}
					fmt.Println("item after manually modification:")
					spew.Dump(item)

					fmt.Println("Terraform code:")
					fmt.Println(integrationLoginProviderConfig("http://localhost:8080") + integrationLoginResourceConfig(expectedItem))
				},
				Config: integrationLoginProviderConfig("http://localhost:8080") + integrationLoginResourceConfig(expectedItem),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("onepassword_item.test-database", "title", expectedItem.Title),
					resource.TestCheckResourceAttr("onepassword_item.test-database", "category", strings.ToLower(string(expectedItem.Category))),
					resource.TestCheckResourceAttr("onepassword_item.test-database", "username", manuallySetUsername),
					resource.TestCheckResourceAttr("onepassword_item.test-database", "url", manuallySetUrl),
					resource.TestCheckResourceAttrSet("onepassword_item.test-database", "password"),
					func(*terraform.State) error {
						item, err := getItemByName("http://localhost:8080", token, target_vault, title)
						if err != nil || item == nil {
							return err
						}
						//fmt.Println("item after Terraform apply:")
						//spew.Dump(item)
						if len(item.URLs) != 1 {
							return fmt.Errorf("expected 1 URL after apply, got %d", len(item.URLs))
						} else {
							if item.URLs[0].URL != manuallySetUrl {
								return fmt.Errorf("expected URL to be %s, got %s", manuallySetUrl, item.URLs[0].URL)
							}
						}
						f := getFieldByName(item, "username")
						if f != nil && f.Value != manuallySetUsername {
							fmt.Errorf("expected username to be %s, got %s", manuallySetUsername, f.Value)
						}
						return nil
					},
				),
			},
			{
				PreConfig: func() {
					fmt.Println("3. step\noverride the username with Terraform\nTerraform code:")
					fmt.Println(integrationLoginProviderConfig("http://localhost:8080") + integrationLoginResourceConfig(expectedItemUpdate))
				},
				Config: integrationLoginProviderConfig("http://localhost:8080") + integrationLoginResourceConfig(expectedItemUpdate),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("onepassword_item.test-database", "title", expectedItemUpdate.Title),
					resource.TestCheckResourceAttr("onepassword_item.test-database", "category", strings.ToLower(string(expectedItemUpdate.Category))),
					resource.TestCheckResourceAttr("onepassword_item.test-database", "username", "test_user"),
					resource.TestCheckResourceAttr("onepassword_item.test-database", "url", expectedItemUpdate.URLs[0].URL),
					resource.TestCheckResourceAttrSet("onepassword_item.test-database", "password"),
				),
			},
			{
				PreConfig: func() {
					fmt.Println("4. step\nunset the username with Terraform\nTerraform code:")
					fmt.Println(integrationLoginProviderConfig("http://localhost:8080") + integrationLoginResourceConfig(expectedItemUpdateReset))
				},
				Config: integrationLoginProviderConfig("http://localhost:8080") + integrationLoginResourceConfig(expectedItemUpdateReset),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("onepassword_item.test-database", "title", expectedItemUpdateReset.Title),
					resource.TestCheckResourceAttr("onepassword_item.test-database", "category", strings.ToLower(string(expectedItemUpdateReset.Category))),
					resource.TestCheckResourceAttr("onepassword_item.test-database", "username", ""),
					//resource.TestCheckResourceAttr("onepassword_item.test-database", "url", expectedItemUpdateReset.URLs[0].URL),
					resource.TestCheckResourceAttr("onepassword_item.test-database", "url", ""),
					resource.TestCheckResourceAttrSet("onepassword_item.test-database", "password"),
					func(*terraform.State) error {
						item, err := getItemByName("http://localhost:8080", token, target_vault, title)
						if err != nil || item == nil {
							return err
						}
						fmt.Println("item after Terraform apply:")
						spew.Dump(item)
						//fmt.Println("sleeping ....")
						//time.Sleep(30 * time.Second)
						return nil
					},
				),
			},
		},
	})
}

func getItemByName(connectUrl, token, vault, itemName string) (*onepassword.Item, error) {
	client := connect.NewClient(connectUrl, token)
	v, err := client.GetVaultByTitle(vault)
	if err != nil {
		return nil, err
	}
	item, err := client.GetItem(itemName, v.ID)
	if err != nil {
		return nil, err
	}
	return item, nil
}
func setItem(connectUrl, token, vault string, newItem *onepassword.Item) (*onepassword.Item, error) {
	client := connect.NewClient(connectUrl, token)
	v, err := client.GetVaultByTitle(vault)
	if err != nil {
		return nil, err
	}
	item, err := client.UpdateItem(newItem, v.ID)
	if err != nil {
		return nil, err
	}
	return item, nil
}

func integrationLoginResourceConfig(expectedItem *onepassword.Item) string {
	username := "null"
	usernameField := getFieldByName(expectedItem, "username")
	if usernameField != nil {
		username = "\"" + usernameField.Value + "\""
	}
	url := "null"
	if len(expectedItem.URLs) > 0 {
		url = "\"" + expectedItem.URLs[0].URL + "\""
	}

	return fmt.Sprintf(`

data "onepassword_vault" "acceptance-tests" {
  name = "%s"
}
resource "onepassword_item" "test-database" {
  vault = data.onepassword_vault.acceptance-tests.uuid
  title = "%s"
  category = "%s"
  username = %s
  password_recipe {}
  url = %s
}
output "item" {
  value = onepassword_item.test-database
  sensitive = true
}
`, target_vault /*expectedItem.Vault.ID*/, expectedItem.Title, strings.ToLower(string(expectedItem.Category)), username, url)
}

func integrationLoginProviderConfig(url string) string {
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

func getFieldByName(item *onepassword.Item, label string) *onepassword.ItemField {
	for _, f := range item.Fields {
		if f == nil {
			continue
		}
		if f.Label == label {
			return f
		}
	}
	return nil
}

func removeField(slice []*onepassword.ItemField, s int) []*onepassword.ItemField {
	return append(slice[:s], slice[s+1:]...)
}
