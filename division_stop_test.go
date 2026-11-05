package main

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/require"
)

const zeroDivisorLib = `res = Quo(x, n)
    res = x ÷ n

p, q = Pair(x, y)
    p = x
    q = y
`

// buildZeroDivisorPrograms compiles one script per case into a fresh module and
// returns its directory. Each script prints "before", runs body, then prints
// "after", which a stop must keep from printing.
func buildZeroDivisorPrograms(t *testing.T, cases []zeroDivisorCase) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("PTCACHE", filepath.Join(t.TempDir(), "cache"))

	require.NoError(t, os.WriteFile(filepath.Join(dir, MOD_FILE), []byte("module zerodivisor\n"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "lib"+PT_SUFFIX), []byte(zeroDivisorLib), 0644))
	for _, tc := range cases {
		script := "\"before\"\n" + tc.body + "\n\"after\"\n"
		require.NoError(t, os.WriteFile(filepath.Join(dir, tc.name+SPT_SUFFIX), []byte(script), 0644))
	}

	runCompile(cliOptions{target: dir})
	return dir
}

type zeroDivisorCase struct {
	name, body string
}

func TestZeroDivisorStopsTheProgram(t *testing.T) {
	cases := []zeroDivisorCase{
		{"literal_quotient", "x = 7 ÷ 0\nx"},
		{"literal_remainder", "x = 7 % 0\nx"},
		{"read_quotient", "v = [7 0]\nx = v[0] ÷ v[1]\nx"},
		{"read_remainder", "v = [7 0]\nx = v[0] % v[1]\nx"},
		{"call", "v = [7 0]\nx = Quo(v[0], v[1])\nx"},
		{"range_cell", "r = 0:3\n[7 ÷ r]"},
		{"array_cells", "x = [8 8] ÷ [0 2]\nx"},
		{"scalar_by_array", "d = [0 2]\nx = 8 ÷ d\nx"},
		{"array_mask_keeps_zeros", "d = [0 2]\nx = [8 8] ÷ (d != 0)\nx"},
		{"loop_after_good_read", "d = [1 0]\nk = 0:2\ns = 0\ns = s + 7 ÷ d[k]\ns"},
		{"cell_after_good_read", "d = [1 0]\nk = 0:2\n[7 ÷ d[k]]"},
		{"tuple_in_comparison", "v = [7 0]\na = v[0]\nn = v[1]\np, q = (Pair(a, a) ÷ Pair(1, n)) > Pair(0, 0)\np, q"},
		{"check_on_its_own_line", "v = [7 0]\nn = v[1]\ny = n != 0 1\nx = v[0] ÷ n\ny, x"},
		{"stale_flag", "v = [7 0]\nn = v[1]\nok = 1\nok = n != 0 1\nx = 5\nx = ok == 1 Quo(v[0], n)\nok, x"},
		// PIR's checks-first work is planned to make these two keep going.
		{"filtered_argument_in_comparison", "v = [7 0]\nx = 5\nx = Quo(v[0], v[1] != 0) > 0\nx"},
		{"call_past_the_end_in_cell", "d = [1 2]\nk = 0:4\n[Quo(7, d[k])]"},
	}
	dir := buildZeroDivisorPrograms(t, cases)

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			binary := filepath.Join(dir, tc.name)
			if runtime.GOOS == OS_WINDOWS {
				binary += EXE_SUFFIX
			}
			var stdout, stderr bytes.Buffer
			cmd := exec.Command(binary)
			cmd.Stdout = &stdout
			cmd.Stderr = &stderr

			var exit *exec.ExitError
			require.True(t, errors.As(cmd.Run(), &exit), "the program must not exit normally")
			require.Equal(t, "pluto: division by zero\n", unixNewlines(stderr.String()))
			require.Equal(t, "before\n", unixNewlines(stdout.String()), "output printed before the stop must survive it")
		})
	}
}
