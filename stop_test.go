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

const stopLib = `res = Quo(x, n)
    res = x ÷ n

p, q = Pair(x, y)
    p = x
    q = y

res = Upto(n)
    res = [0:n]
`

// buildStopPrograms compiles one script per case into a fresh module and
// returns its directory. Each script prints "before", runs body, then prints
// "after", which a stop must keep from printing.
func buildStopPrograms(t *testing.T, cases []stopCase) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("PTCACHE", filepath.Join(t.TempDir(), "cache"))

	require.NoError(t, os.WriteFile(filepath.Join(dir, MOD_FILE), []byte("module stops\n"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "lib"+PT_SUFFIX), []byte(stopLib), 0644))
	for _, tc := range cases {
		script := "\"before\"\n" + tc.body + "\n\"after\"\n"
		require.NoError(t, os.WriteFile(filepath.Join(dir, tc.name+SPT_SUFFIX), []byte(script), 0644))
	}

	runCompile(cliOptions{target: dir})
	return dir
}

// runStoppedProgram runs the named program, requires that it stopped without
// losing what it printed before the stop, and returns its stderr.
func runStoppedProgram(t *testing.T, dir, name string) string {
	t.Helper()
	binary := filepath.Join(dir, name)
	if runtime.GOOS == OS_WINDOWS {
		binary += EXE_SUFFIX
	}
	var stdout, stderr bytes.Buffer
	cmd := exec.Command(binary)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	var exit *exec.ExitError
	require.True(t, errors.As(cmd.Run(), &exit), "the program must not exit normally")
	require.Equal(t, "before\n", unixNewlines(stdout.String()), "output printed before the stop must survive it")
	return unixNewlines(stderr.String())
}

type stopCase struct {
	name, body string
}

func TestZeroDivisorStopsTheProgram(t *testing.T) {
	cases := []stopCase{
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
		{"tuple_failed_slot_beside_zero", "v = [7 0]\na = v[0]\nn = v[1]\np = 99\nq = 88\np, q = (Pair(a, a) ÷ (Pair(n, n) != Pair(0, 5))) > Pair(0, 0)\np, q"},
		{"tuple_zero_beside_failed_slot", "v = [7 0]\na = v[0]\nn = v[1]\np = 99\nq = 88\np, q = (Pair(a, a) ÷ (Pair(n, n) != Pair(5, 0))) > Pair(0, 0)\np, q"},
		{"tuple_fallback_beside_zero", "v = [7 0]\na = v[0]\nn = v[1]\np, q = ((Pair(a, a) ÷ (Pair(n, n) != Pair(0, 5))) > Pair(0, 0)) || Pair(1, 9)\np, q"},
		{"tuple_call_feeds_passing_slot", "v = [7 0]\na = v[0]\nn = v[1]\np = 99\nq = 88\np, q = Pair(a ÷ n, 1) > (Pair(n, 1) != Pair(0, 0))\np, q"},
		{"tuple_repeated_zero_feeds_passing_slot", "v = [7 0]\na = v[0]\nn = v[1]\np = 99\nq = 88\np, q = ((a ÷ n) + (Pair(n, 1) != Pair(0, 0))) > Pair(0, 0)\np, q"},
		{"tuple_check_beside_zero", "v = [7 0]\na = v[0]\nn = v[1]\np, q = ((Pair(n, n) != Pair(0, 5)) + (Pair(a, a) ÷ Pair(n, n))) || Pair(1, 9)\np, q"},
		{"resolved_read_in_cell", "d = [1 2]\nv = [0]\nn = v[0]\nx = [((d[9] > 0) || 2) + 7 ÷ n]\nx"},
		{"resolved_read_in_nested_cell", "d = [1 2]\nv = [0]\nn = v[0]\nx = [[((d[9] > 0) || 2) 7 ÷ n]]\nx"},
		{"resolved_check_in_cell", "v = [7 0]\na = v[0]\nn = v[1]\nx = [((a ÷ (n != 0)) || 2) + 7 ÷ n]\nx"},
		{"resolved_check_in_value", "v = [7 0]\na = v[0]\nn = v[1]\nx = ((a ÷ (n != 0)) || 2) + 7 ÷ n\nx"},
		{"failed_cell_beside_zero", "d = [1 2]\nv = [0]\nn = v[0]\nx = [[d[9] 7 ÷ n]]\nx"},
		{"failed_row_beside_zero", "d = [1 2]\nv = [0]\nn = v[0]\nx = [[d[9]] ⊕ [7 ÷ n]]\nx"},
		{"failed_cell_divides_as_zero", "d = [1 2]\nx = [8 8] ÷ [d[9] 2]\nx"},
		{"fallback_right_side", "d = [1 2]\nv = [0]\nn = v[0]\nx = (d[9] > 0) || (7 ÷ n)\nx"},
		{"fallback_right_side_after_check", "v = [0]\nn = v[0]\nx = ((n != 0) + 7) || (7 ÷ n)\nx"},
		{"passing_check_in_fallback", "v = [3 0]\nm = v[0]\nn = v[1]\nx = ((m != 0) + 7 ÷ n) || 6\nx"},
		{"passing_check_in_and", "v = [3 0]\nm = v[0]\nn = v[1]\nx = 5\nx = (m > 0) && ((m != 0) + 7 ÷ n)\nx"},
		{"call_beside_passing_check", "v = [7 0]\nx = 5\nx = Quo(v[0], v[1]) > (v[0] != 0)\nx"},
		{"passing_check_before_a_call", "v = [3 0]\nm = v[0]\nn = v[1]\nx = 5\nx = (m != 0) + (Quo(7, n) > 0)\nx"},
		{"check_needs_the_divide", "v = [0]\nn = v[0]\nx = 5\nx = ((7 ÷ n) > 0) + (n != 0)\nx"},
		{"later_check_other_slot_failed", "v = [7 0]\na = v[0]\nn = v[1]\np, q = 8, 9\np, q = (Pair(1, 1) > Pair(2, n)) + ((Pair(a, a) ÷ Pair(1, n)) > 0)\np, q"},
		{"later_check_both_slots_passed", "v = [7 0]\na = v[0]\nn = v[1]\np, q = 8, 9\np, q = (Pair(1, 1) > Pair(n, n)) + ((Pair(a, a) ÷ Pair(n, 1)) > 0)\np, q"},
		{"later_check_repeated_divide", "v = [0]\nn = v[0]\np, q = 8, 9\np, q = (Pair(1, 1) > Pair(2, n)) + ((7 ÷ n) > 0)\np, q"},
		{"later_call_feeds_passing_slot", "v = [0]\nn = v[0]\np, q = 8, 9\np, q = (Pair(1, 1) > Pair(2, n)) + Pair(Quo(7, n), 1)\np, q"},
		{"later_check_call_feeds_passing_slot", "v = [0]\nn = v[0]\np, q = 8, 9\np, q = (Pair(1, 1) > Pair(2, n)) + (Pair(Quo(7, n), 1) > 0)\np, q"},
		{"later_check_after_resolved_slot", "v = [7 0]\na = v[0]\nn = v[1]\np, q = 8, 9\np, q = ((Pair(1, 1) > Pair(2, n)) || Pair(5, 5)) + ((Pair(a, a) ÷ Pair(n, 1)) > 0)\np, q"},
		{"fallback_needed_by_passing_slot", "v = [0]\nn = v[0]\np, q = 8, 9\np, q = (Pair(1, 1) > Pair(n, n)) + ((Pair(1, 1) > Pair(2, n)) || Quo(7, n))\np, q"},
		{"argument_check_passes_before_zero", "v = [7 0]\na = v[0]\nn = v[1]\np, q = 8, 9\np, q = Pair(1 > n, 1) + ((Pair(a, a) ÷ Pair(n, n)) > 0)\np, q"},
		{"failed_slot_under_a_gate", "v = [7 0]\na = v[0]\nn = v[1]\nx = 5\nx = ((Pair(1, 1) > Pair(2, n)) + ((Pair(a, a) ÷ Pair(1, n)) > 0)) > 0 6\nx"},
		// PIR's checks-first work is planned to make these keep going.
		{"read_after_the_divide", "d = [1 2]\nv = [0]\nn = v[0]\nx = 5\nx = (7 ÷ n + d[9]) > 0\nx"},
		{"read_beside_a_later_check", "d = [1 2]\nv = [0]\nn = v[0]\nx = 5\nx = (d[9] + 1) + ((7 ÷ n) > 0)\nx"},
		{"fallback_before_a_failed_check", "v = [7 0]\na = v[0]\nn = v[1]\nx = 5\nx = ((a > 9) || Quo(7, n)) + (n != 0)\nx"},
		{"ranged_check_before_a_divide", "v = [0]\nn = v[0]\ni = 0:2\nx = 5\nx = (i > 5) + ((7 ÷ n) > 0)\nx"},
		{"need_stops_at_a_comparison", "v = [0]\nn = v[0]\np, q = 8, 9\np, q = (Pair(1, 1) > Pair(2, n)) + (((Pair(1, 1) > Pair(2, n)) || (7 ÷ n)) > -99)\np, q"},
	}
	dir := buildStopPrograms(t, cases)

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, "pluto: division by zero\n", runStoppedProgram(t, dir, tc.name))
		})
	}
}

func TestShapeMismatchStopsTheProgram(t *testing.T) {
	cases := []stopCase{
		{"rows_differ", "a = [1 2]\nb = [1 2 3]\nx = [a b]\nx"},
		{"ranged_rows_differ", "i = 2:4\nx = [Upto(i)]\nx"},
		{"empty_row_is_a_row", "d = [1 2]\nw = [8 8] ÷ d[9]\na = [1 2]\nx = [w a]\nx"},
		{"failed_row_keeps_its_shape", "a = [1 2]\nb = [1 2 3]\nd = [1 2]\nx = [a ÷ d[9] b]\nx"},
		{"failed_empty_row_keeps_its_shape", "a = [1]\nd = [1 2]\nx = [d[9] > 0 && []a  [[1]]]\nx"},
	}
	dir := buildStopPrograms(t, cases)

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Regexp(t, `^pluto: array shape mismatch: expected dimension \d+, got \d+\n$`, runStoppedProgram(t, dir, tc.name))
		})
	}
}
