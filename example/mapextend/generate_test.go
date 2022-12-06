package mapextend

import (
	"fmt"
	"github.com/jmattheis/goverter"
	"os"
	"testing"
)

func TestGen(t *testing.T) {

	err := goverter.GenerateConverterFile("./generated/generated.go", goverter.GenerateConfig{
		PackageName: "generated",
		ScanDir:     "github.com/jmattheis/goverter/example/mapextend",
		PackagePath: "",
	})

	if err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

}
