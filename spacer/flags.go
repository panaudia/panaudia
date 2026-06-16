package spacer

/*
#cgo LDFLAGS: -L /usr/local/lib -lsaf_example_ambi_bin
#cgo LDFLAGS: -L /usr/local/lib -lsaf
#cgo openblas LDFLAGS: -L /usr/local/lib -L /usr/local/opt/openblas/lib -lopenblas
#cgo lapacke LDFLAGS: -L /usr/local/lib -llapacke
#cgo ipp LDFLAGS: -L /usr/local/lib -lsaf_ipp_custom
#cgo ipp LDFLAGS: -L /usr/local/lib -lsaf_mkl_custom_lp64
#cgo accelerate LDFLAGS: -framework Accelerate
#cgo LDFLAGS: -L /usr/local/lib -lpanaudia_utils
#cgo LDFLAGS: -Wl,-rpath,/usr/local/lib
#cgo LDFLAGS: -O2
#cgo LDFLAGS: -lm
*/
import "C"
