package wasm

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path"

	"cuelang.org/go/cue"
	"cuelang.org/go/cue/ast"
	cuejson "cuelang.org/go/encoding/json"
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
	base := path.Base(file)
	return extism.Manifest{
		Wasm: []extism.Wasm{
			extism.WasmFile{
				Path: file,
				Name: base,
			},
		},
	}
}

func TransformValueByUrl(url, function string, input cue.Value) (ast.Expr, error) {

	manifest := getManifestByUrl(url)
	return execute(manifest, function, input)
}

func TransformValueByFile(file, function string, input cue.Value) (ast.Expr, error) {

	manifest := getManifestByFile(file)
	return execute(manifest, function, input)
}

func execute(manifest extism.Manifest, function string, input cue.Value) (ast.Expr, error) {

	ctx := context.Background()
	config := extism.PluginConfig{EnableWasi: true}
	plugin, err := extism.NewPlugin(ctx, manifest, config, []extism.HostFunction{})
	if err != nil {
		return ast.NewNull(), fmt.Errorf("failed to create plugin, %w", err)
	}
	jsonData, err := json.Marshal(input)
	if err != nil {
		return ast.NewNull(), fmt.Errorf("failed to marshal input, %w", err)
	}
	fmt.Println("jsonData", string(jsonData))
	exit, out, err := plugin.Call(function, jsonData)
	if err != nil {
		fmt.Println(err)
		os.Exit(int(exit))
	}
	decoded, err := cuejson.Extract("", out)
	if err != nil {
		return ast.NewNull(), fmt.Errorf("decoding failed, %w", err)
	}
	return decoded, nil
}
