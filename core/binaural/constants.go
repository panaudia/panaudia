package binaural

// these constants are used by the BinauralDecoder

const AMBI_BIN_ENABLE_MAX_RE = 1
const AMBI_BIN_ENABLE_DIFFUSE_MATCHING = 0
const AMBI_BIN_ENABLE_TRUNCATION_EQ = 0

/**
 * Available Ambisonic channel ordering conventions
 *
 * @warning CH_FUMA is only supported for first order input!
 */

const (
	CH_ORDER_ACN  = iota + 1 /**< Ambisonic Channel Numbering (ACN) */
	CH_ORDER_FUMA = iota + 1 /**< (Legacy) Furse-Malham/B-format (WXYZ) */
)
const AMBI_CH_ORDER = CH_ORDER_ACN

/**
 * Available Ambisonic normalisation conventions
 *
 * @warning NORM_FUMA is only supported for first order input! It also  has the
 *          1/sqrt(2) scaling term applied to the omni.
 */

const (
	NORM_N3D  = iota + 1 /**< orthonormalised (N3D) */
	NORM_SN3D = iota + 1 /**< Schmidt semi-normalisation (SN3D) */
	NORM_FUMA = iota + 1 /**< (Legacy) Furse-Malham scaling */
)
const AMBI_NORMALISATION_TYPE = NORM_N3D

const (
	DECODING_METHOD_LS       = iota + 1 /**< Least-squares (LS) decoder */
	DECODING_METHOD_LSDIFFEQ = iota + 1 /**< Least-squares (LS) decoder with diffuse-field spectral equalisation */
	DECODING_METHOD_SPR      = iota + 1 /**< Spatial resampling decoder (on the same lines as the virtual loudspeaker approach) */
	DECODING_METHOD_TA       = iota + 1 /**< Time-alignment (TA) */
	DECODING_METHOD_MAGLS    = iota + 1 /**< Magnitude least-squares decoder (MagLS) */
)
const AMBI_BIN_DECODING_METHOD = DECODING_METHOD_MAGLS

var DECODING_METHOD_NAMES = map[int]string{
	DECODING_METHOD_LS:       "Least-squares",
	DECODING_METHOD_LSDIFFEQ: "Least-squares with diffuse-field spectral equalisation",
	DECODING_METHOD_SPR:      "Spatial resampling",
	DECODING_METHOD_TA:       "Time-alignment",
	DECODING_METHOD_MAGLS:    "Magnitude least-squares",
}

const (
	HRIR_PREPROC_OFF   = iota + 1 /**< No pre-processing active */
	HRIR_PREPROC_EQ    = iota + 1 /**< Diffuse-field EQ (compensates CTF) */
	HRIR_PREPROC_PHASE = iota + 1 /**< Phase simplification based on ITD */
	HRIR_PREPROC_ALL   = iota + 1 /**< Diffuse-field EQ AND phase-simplification */
)
const AMBI_BIN_HRIR_PREPROC = HRIR_PREPROC_ALL
