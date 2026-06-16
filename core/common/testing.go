package common

import (
	"fmt"
	"math"
	"testing"
)

func AssertArraysEqual(t *testing.T, A []float32, B []float32) {

	if len(A) != len(B) {
		t.Fatalf(`AssertArraysEqual length different`)
	}

	for i, v := range A {
		if v != B[i] {
			t.Fatalf(`arrays not equal - got: %v expected: %v`, A, B)
		}
	}
}

func AssertApproxArraysEqual(t *testing.T, A []float32, B []float32) {

	if len(A) != len(B) {
		t.Fatalf(`AssertArraysEqual length different`)
	}

	for i, v := range A {
		if math.Abs(float64(v-B[i])) > 0.00001 {
			fmt.Printf(`%d %v %v\n`, i, v, B[i])
			t.Fatalf(`arrays not equal - got: %v expected: %v`, A, B)
		}
	}
}

func AssertApproxishArraysEqual(t *testing.T, A []float32, B []float32) {

	if len(A) != len(B) {
		t.Fatalf(`AssertArraysEqual length different`)
	}

	for i, v := range A {
		if math.Abs(float64(v-B[i])) > 0.01 {
			t.Fatalf(`arrays not equal - got: %v expected: %v`, A, B)
		}
	}
}

func AssertArraysEqualInt(t *testing.T, A []int, B []int) {

	if len(A) != len(B) {
		t.Fatalf(`AssertArraysEqual length different`)
	}

	for i, v := range A {
		if v != B[i] {
			t.Fatalf(`arrays not equal - got: %v expected: %v`, A, B)
		}
	}
}

func AssertArraysAlmostEqual(t *testing.T, A []float32, B []float32) {

	if len(A) != len(B) {
		t.Fatalf(`AssertArraysEqual length different`)
	}

	for i, v := range A {
		if math.Abs(float64(v-B[i])) > 0.000001 {
			fmt.Printf(`%v %v`, v, B[i])
			t.Fatalf(`arrays not almost equal - got: %v expected: %v`, A, B)
		}
	}
}
