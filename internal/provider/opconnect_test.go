package provider

import (
	"encoding/base64"
	"errors"
	"fmt"
	"github.com/1Password/connect-sdk-go/connect"
	"github.com/1Password/connect-sdk-go/onepassword"
	"github.com/avast/retry-go/v4"
	"github.com/gruntwork-io/terratest/modules/aws"
	"github.com/gruntwork-io/terratest/modules/docker"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
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

func TestOpconnect(t *testing.T) {

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

	expectedItem := generateLoginItem()
	expectedItem.Fields = removeField(expectedItem.Fields, 0)
	expectedItem.Fields = removeField(expectedItem.Fields, 0)

	expectedItemUpdate := generateLoginItem()

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
					fmt.Println(testAccProviderConfig("http://localhost:8080") + testAccLoginResourceConfig(expectedItem))
				},
				Config: testAccProviderConfig("http://localhost:8080" /*testServer.URL*/) + testAccLoginResourceConfig(expectedItem),
				Check: resource.ComposeAggregateTestCheckFunc(
					// verify local values
					resource.TestCheckResourceAttr("onepassword_item.test-database", "title", expectedItem.Title),
					resource.TestCheckResourceAttr("onepassword_item.test-database", "category", strings.ToLower(string(expectedItem.Category))),
					resource.TestCheckResourceAttr("onepassword_item.test-database", "username", ""),
					resource.TestCheckResourceAttr("onepassword_item.test-database", "url", expectedItem.URLs[0].URL),
					resource.TestCheckResourceAttrSet("onepassword_item.test-database", "password"),
				),
			},
			{
				PreConfig: func() {
					fmt.Println("2. step\noverride the username manually\nTerraform code:")
					fmt.Println(testAccProviderConfig("http://localhost:8080") + testAccLoginResourceConfig(expectedItem))

					item, err := getItemByName("http://localhost:8080", token, target_vault, "test item")
					if err != nil {
						t.Error(err)
					}
					f := getFieldByName(item, "username")
					f.Value = "manually set"
					item, err = setItem("http://localhost:8080", token, target_vault, item)
					if err != nil {
						t.Error(err)
					}
				},
				Config: testAccProviderConfig("http://localhost:8080" /*testServerRedeploy.URL*/) + testAccLoginResourceConfig(expectedItem),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("onepassword_item.test-database", "username", "manually set"),
				),
			},
			{
				PreConfig: func() {
					fmt.Println("3. step\noverride the username with Terraform\nTerraform code:")
					fmt.Println(testAccProviderConfig("http://localhost:8080") + testAccLoginResourceConfig(expectedItemUpdate))
				},
				Config: testAccProviderConfig("http://localhost:8080" /*testServerRedeploy.URL*/) + testAccLoginResourceConfig(expectedItemUpdate),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("onepassword_item.test-database", "username", "test_user"),
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
