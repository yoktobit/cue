package wasm

import (
	"context"
	"fmt"
	"os"

	extism "github.com/extism/go-sdk"
)

func getManifestByUrl(url string) extism.Manifest {
	return extism.Manifest{
		Wasm: []extism.Wasm{
			extism.WasmUrl{
				Url: url,
			},
		},
	}
}

func getManifestByFile(file string) extism.Manifest {
	return extism.Manifest{
		Wasm: []extism.Wasm{
			extism.WasmFile{
				Name: file,
			},
		},
	}
}

func ExecuteUrl(url, function, s string) (string, error) {

	manifest := getManifestByUrl(url)
	return Execute(manifest, function, s)
}

func ExecuteFile(file, function, s string) (string, error) {

	manifest := getManifestByFile(file)
	return Execute(manifest, function, s)
}

func Execute(manifest extism.Manifest, function, s string) (string, error) {

	ctx := context.Background()
	config := extism.PluginConfig{}
	plugin, err := extism.NewPlugin(ctx, manifest, config, []extism.HostFunction{})

	if err != nil {
		fmt.Printf("Failed to initialize plugin: %v\n", err)
		os.Exit(1)
	}

	data := []byte(s)
	exit, out, err := plugin.Call(function, data)
	if err != nil {
		fmt.Println(err)
		os.Exit(int(exit))
	}

	response := string(out)
	return response, nil
}
