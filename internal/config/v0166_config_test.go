package config

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestV0166ConfigFileOverridesSearchPaths(t *testing.T) {
	t.Setenv("CONFIG_FILE", "  /tmp/tokenav-explicit.yaml  ")
	t.Setenv("DATA_DIR", "/tmp/ignored-data")
	var selected string
	var paths []string

	configureConfigSource(func(value string) { selected = value }, func(value string) { paths = append(paths, value) })

	require.Equal(t, "/tmp/tokenav-explicit.yaml", selected)
	require.Empty(t, paths, "显式配置文件存在时不能再搜索其他目录")
}

func TestV0166ConfigSearchPathsKeepDataDirPriority(t *testing.T) {
	t.Setenv("CONFIG_FILE", "")
	t.Setenv("DATA_DIR", " /tmp/tokenav-data ")
	var selected string
	var paths []string

	configureConfigSource(func(value string) { selected = value }, func(value string) { paths = append(paths, value) })

	require.Empty(t, selected)
	require.Equal(t, []string{"/tmp/tokenav-data", "/app/data", ".", "./config", "/etc/sub2api"}, paths)
}
