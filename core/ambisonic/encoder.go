package ambisonic

import (
	"math"
	"unsafe"

	mapset "github.com/deckarep/golang-set/v2"
	"github.com/google/uuid"
	"github.com/panaudia/panaudia/core/common"
	"github.com/panaudia/panaudia/spacer"
)

const SQRT_4_PI float32 = 3.5449077018110318

type Encoder struct {
	Uuid                    uuid.UUID
	Position                common.Position
	slot                    int
	hasInput                bool
	gain                    float64
	gainFactor              float32
	attenuation             float64
	peerAttenuationExponent float64
	Input                   []float32
	weightsBufferA          []float32
	weightsBufferB          []float32
	reverbWeightsBufferA    []float32
	reverbWeightsBufferB    []float32
	sphericalHarmonics      []float32
	bufferACurrent          bool
	ReverbOutput            []float32
	Output                  []float32
	mixerConfig             common.MixerConfig
	reverb                  *SimpleReverb

	SubSpaces mapset.Set[uuid.UUID]
	Mutes     mapset.Set[uuid.UUID]
	Solos     mapset.Set[uuid.UUID]

	// Roles this entity holds. Source-of-truth set, mirroring the
	// {uuid}.roles.{R} flat keys on the entity topic.
	Roles mapset.Set[string]
	// MuteRoles is the listener-side personal "mute by role" set
	// ({me}.mute-roles.{R}). Sources whose Roles intersect this set are
	// vetoed for *this* listener.
	MuteRoles mapset.Set[string]
	// SpaceMuted is true when {uuid}.muted is set on the entity topic
	// (space-wide entity mute on this source).
	SpaceMuted bool

	// Cached for fast-path use in shouldIncludePeer: true iff any role
	// in Roles is also in BaseSpace.mutedRoles. Maintained by
	// BaseSpace.RefreshEncoderRoleEffects.
	spaceRoleMuted bool
	// Cached role-derived multiplier on entity gain (default 1.0).
	roleGainMultiplier float64
	// Cached role-derived attenuation override; nil = no override.
	// When set, replaces (not multiplies) the entity attenuation.
	roleAttenuationOverride *float64

	filteredPeers []*Encoder
}

func NewEncoder(
	Uuid uuid.UUID,
	hasInput bool,
	gain float64,
	attenuation float64,
	mixerConfig common.MixerConfig,
	slot int) *Encoder {

	encoder := Encoder{}
	encoder.gain = gain
	encoder.gainFactor = float32(math.Pow(gain, 0.5)) * SQRT_4_PI
	encoder.Uuid = Uuid
	encoder.slot = slot
	encoder.hasInput = hasInput
	encoder.attenuation = attenuation
	encoder.peerAttenuationExponent = attenuation / 2.0
	encoder.mixerConfig = mixerConfig
	encoder.reverb = NewSimpleReverb(mixerConfig.FrameSize, mixerConfig.ChannelCount, 48000.0)

	switch mixerConfig.ReverbPreset {
	case common.REVERB_TIGHT_ROOM:
		encoder.reverb.SetPresetTightRoom()
	case common.REVERB_SMALL_ROOM:
		encoder.reverb.SetPresetSmallRoom()
	case common.REVERB_MEDIUM_ROOM:
		encoder.reverb.SetPresetMediumRoom()
	case common.REVERB_LARGE_HALL:
		encoder.reverb.SetPresetLargeHall()
	case common.REVERB_CATHEDRAL:
		encoder.reverb.SetPresetCathedral()
	default:

	}

	encoder.SubSpaces = mapset.NewSet[uuid.UUID]()
	encoder.Mutes = mapset.NewSet[uuid.UUID]()
	encoder.Solos = mapset.NewSet[uuid.UUID]()
	encoder.Roles = mapset.NewSet[string]()
	encoder.MuteRoles = mapset.NewSet[string]()
	encoder.roleGainMultiplier = 1.0

	encoder.Input = make([]float32, mixerConfig.FrameSize)
	encoder.ReverbOutput = make([]float32, mixerConfig.FrameSize*common.REVERB_CHANNELS)
	encoder.Output = make([]float32, mixerConfig.FrameSize*mixerConfig.ChannelCount)
	encoder.weightsBufferA = make([]float32, mixerConfig.MaxNodes*mixerConfig.ChannelCount)
	encoder.weightsBufferB = make([]float32, mixerConfig.MaxNodes*mixerConfig.ChannelCount)
	encoder.reverbWeightsBufferA = make([]float32, mixerConfig.MaxNodes*mixerConfig.ChannelCount)
	encoder.reverbWeightsBufferB = make([]float32, mixerConfig.MaxNodes*mixerConfig.ChannelCount)
	encoder.bufferACurrent = true
	encoder.filteredPeers = make([]*Encoder, mixerConfig.MaxNodes)
	encoder.sphericalHarmonics = make([]float32, mixerConfig.ChannelCount)

	return &encoder
}

func (encoder *Encoder) SetPosition(position common.Position) {
	encoder.Position = position
}

func (encoder *Encoder) ApplyReverb() bool {
	return encoder.mixerConfig.ReverbPreset != common.REVERB_NONE
}

func (encoder *Encoder) AddSubSpace(id uuid.UUID) {
	encoder.SubSpaces.Add(id)
}

func (encoder *Encoder) RemoveSubSpace(id uuid.UUID) {
	encoder.SubSpaces.Remove(id)
}

func (encoder *Encoder) AddSolo(id uuid.UUID) {
	encoder.Solos.Add(id)
}

func (encoder *Encoder) RemoveSolo(id uuid.UUID) {
	encoder.Solos.Remove(id)
}

func (encoder *Encoder) AddMute(id uuid.UUID) {
	encoder.Mutes.Add(id)
}

func (encoder *Encoder) RemoveMute(id uuid.UUID) {
	encoder.Mutes.Remove(id)
}

// SetGain replaces the entity's configured gain and recomputes gainFactor.
// Composes multiplicatively with the current cached roleGainMultiplier so
// any active role-gain stays applied across the change.
func (encoder *Encoder) SetGain(gain float64) {
	encoder.gain = gain
	encoder.recomputeGainFactor()
}

// SetAttenuation replaces the entity's configured attenuation and
// recomputes peerAttenuationExponent. If a roleAttenuationOverride is
// active it stays in effect (override, not compose).
func (encoder *Encoder) SetAttenuation(attenuation float64) {
	encoder.attenuation = attenuation
	encoder.recomputeAttenuationExponent()
}

func (encoder *Encoder) AddRole(role string) {
	encoder.Roles.Add(role)
}

func (encoder *Encoder) RemoveRole(role string) {
	encoder.Roles.Remove(role)
}

func (encoder *Encoder) AddMuteRole(role string) {
	encoder.MuteRoles.Add(role)
}

func (encoder *Encoder) RemoveMuteRole(role string) {
	encoder.MuteRoles.Remove(role)
}

func (encoder *Encoder) SetSpaceMuted(muted bool) {
	encoder.SpaceMuted = muted
}

// SetRoleGainMultiplier sets the cached min-of-applicable-role-gains
// multiplier (1.0 means "no role-gain overrides"). Recomputes gainFactor.
// Owned by BaseSpace.RefreshEncoderRoleEffects; not for direct app use.
func (encoder *Encoder) SetRoleGainMultiplier(m float64) {
	if m <= 0 {
		m = 1.0
	}
	encoder.roleGainMultiplier = m
	encoder.recomputeGainFactor()
}

// SetRoleAttenuationOverride installs (or clears) a role-derived
// attenuation override. Pass nil to clear. Recomputes peerAttenuationExponent.
func (encoder *Encoder) SetRoleAttenuationOverride(att *float64) {
	encoder.roleAttenuationOverride = att
	encoder.recomputeAttenuationExponent()
}

// SetSpaceRoleMuted is the cached "any of my roles is in BaseSpace.mutedRoles"
// flag. Maintained by BaseSpace.RefreshEncoderRoleEffects.
func (encoder *Encoder) SetSpaceRoleMuted(muted bool) {
	encoder.spaceRoleMuted = muted
}

// GainFactor returns the (composed) cached gain factor used during
// mixing. Read-only; callers must not mutate.
func (encoder *Encoder) GainFactor() float32 {
	return encoder.gainFactor
}

// PeerAttenuationExponent returns the (override-aware) cached attenuation
// exponent used by GetWeights / GetWeightsForReverb.
func (encoder *Encoder) PeerAttenuationExponent() float64 {
	return encoder.peerAttenuationExponent
}

// SpaceRoleMuted reports whether the encoder is currently flagged as
// muted by space-wide role mute (any of its Roles ∈ BaseSpace.mutedRoles).
func (encoder *Encoder) SpaceRoleMuted() bool {
	return encoder.spaceRoleMuted
}

func (encoder *Encoder) recomputeGainFactor() {
	effective := encoder.gain * encoder.roleGainMultiplier
	if effective < 0 {
		effective = 0
	}
	encoder.gainFactor = float32(math.Pow(effective, 0.5)) * SQRT_4_PI
}

func (encoder *Encoder) recomputeAttenuationExponent() {
	att := encoder.attenuation
	if encoder.roleAttenuationOverride != nil {
		att = *encoder.roleAttenuationOverride
	}
	encoder.peerAttenuationExponent = att / 2.0
}

func (encoder *Encoder) EncodePeers(peers []*Encoder, dryMixer *Mixer, reverbMixer *Mixer) {

	//common.LogDebug("Peers: %d", len(peers))

	if len(peers) == 0 {
		encoder.ClearOutput()
		return
	}

	encoder.bufferACurrent = !encoder.bufferACurrent
	channelCount := encoder.mixerConfig.ChannelCount

	currentWeightsBuffer := encoder.weightsBufferA
	currentReverbWeightsBuffer := encoder.reverbWeightsBufferA
	previousWeightsBuffer := encoder.weightsBufferB
	previousReverbWeightsBuffer := encoder.reverbWeightsBufferB

	if !encoder.bufferACurrent {
		currentWeightsBuffer = encoder.weightsBufferB
		currentReverbWeightsBuffer = encoder.reverbWeightsBufferB
		previousWeightsBuffer = encoder.weightsBufferA
		previousReverbWeightsBuffer = encoder.reverbWeightsBufferA
	}

	clear(currentWeightsBuffer)
	clear(currentReverbWeightsBuffer)
	encoder.filteredPeers = encoder.filteredPeers[:0]

	for _, peer := range peers {
		if encoder.shouldIncludePeer(peer) {
			encoder.filteredPeers = append(encoder.filteredPeers, peer)
		}
	}

	peerCount := len(encoder.filteredPeers)

	//common.LogDebug("Filtered Peers: %d", peerCount)

	if peerCount == 0 {
		encoder.ClearOutput()
		return
	}

	dryMixer.Reset(peerCount)
	reverbMixer.Reset(peerCount)

	for _, peer := range encoder.filteredPeers {

		//common.LogDebug("encoder.Position:  %v", encoder.Position)
		//common.LogDebug("peer.Position:  %v", peer.Position)
		//common.LogDebug("peer.slot:  %d", peer.slot)
		//common.LogDebug("peer.gainFactor:  %f", peer.gainFactor)
		//common.LogDebug("peer.peerAttenuationExponent %f", peer.peerAttenuationExponent)

		slotIndex := peer.slot * channelCount

		weightsView := currentWeightsBuffer[slotIndex : slotIndex+channelCount]
		previousWeightsView := previousWeightsBuffer[slotIndex : slotIndex+channelCount]

		if encoder.ApplyReverb() {

			// Ony use the first 4 channels for reverb
			reverbWeightsView := currentReverbWeightsBuffer[slotIndex : slotIndex+common.REVERB_CHANNELS]
			previousReverbWeightsView := previousReverbWeightsBuffer[slotIndex : slotIndex+common.REVERB_CHANNELS]

			GetWeightsForReverb(weightsView,
				reverbWeightsView,
				encoder.sphericalHarmonics,
				peer.gainFactor,
				peer.peerAttenuationExponent,
				encoder.Position,
				peer.Position,
				encoder.mixerConfig.Order,
				channelCount,
				encoder.mixerConfig.Size)

			reverbMixer.AddInput(peer.Input, reverbWeightsView, previousReverbWeightsView)

		} else {
			GetWeights(weightsView,
				peer.gainFactor,
				peer.peerAttenuationExponent,
				encoder.Position,
				peer.Position,
				encoder.mixerConfig.Order,
				encoder.mixerConfig.Size)
		}

		dryMixer.AddInput(peer.Input, weightsView, previousWeightsView)
	}

	dryMixer.Mix(encoder.Output)

	if encoder.ApplyReverb() {
		reverbMixer.Mix(encoder.ReverbOutput)
	}

	//common.LogDebug("encoder.Output: %v", encoder.Output)
}

func (encoder *Encoder) PostMix() {
	if encoder.ApplyReverb() {
		encoder.reverb.Apply(encoder.ReverbOutput, encoder.Output)
	}
}

func (encoder *Encoder) shouldIncludePeer(peer *Encoder) bool {

	if peer.Uuid == encoder.Uuid {
		return false
	}

	// Subspace visibility is structural, not a mute. It applies regardless
	// of solo (a soloed source still has to be in a shared subspace).
	if !encoder.SubSpaces.IsEmpty() || !peer.SubSpaces.IsEmpty() {
		if !encoder.SubSpaces.ContainsAnyElement(peer.SubSpaces) {
			return false
		}
	}

	// Solo wins over every mute. If the listener has any solos active, only
	// soloed peers reach them — and a soloed peer bypasses every mute veto.
	if !encoder.Solos.IsEmpty() {
		if !encoder.Solos.Contains(peer.Uuid) {
			return false
		}
		return true
	}

	// Mute vetoes (any of these excludes the peer).
	if peer.SpaceMuted {
		return false
	}
	if peer.spaceRoleMuted {
		return false
	}
	if !encoder.MuteRoles.IsEmpty() && !peer.Roles.IsEmpty() {
		if encoder.MuteRoles.ContainsAnyElement(peer.Roles) {
			return false
		}
	}
	if encoder.Mutes.Contains(peer.Uuid) || peer.Mutes.Contains(encoder.Uuid) {
		return false
	}

	return true
}

func (encoder *Encoder) ClearSource() {

	for i := range encoder.Input {
		encoder.Input[i] = 0
	}
}

func (encoder *Encoder) ClearOutput() {

	for i := range encoder.Output {
		encoder.Output[i] = 0
	}
}

func (encoder *Encoder) AddOtherSource(other *Encoder) {

	for i := range encoder.Input {
		encoder.Input[i] = encoder.Input[i] + other.Input[i]
	}
}

func (encoder *Encoder) AddOtherSink(other *Encoder) {

	for i := range encoder.ReverbOutput {
		encoder.ReverbOutput[i] = encoder.ReverbOutput[i] + other.ReverbOutput[i]
	}

	for i := range encoder.Output {
		encoder.Output[i] = encoder.Output[i] + other.Output[i]
	}
}

func (encoder *Encoder) AddGlobalBuffer(globalBuffer []float32) {

	if encoder.ApplyReverb() {
		for i := range globalBuffer {
			encoder.ReverbOutput[i] = encoder.ReverbOutput[i] + globalBuffer[i]
		}
	} else {
		for i := range globalBuffer {
			encoder.Output[i] = encoder.Output[i] + globalBuffer[i]
		}
	}
}

func GetWeights(weights []float32,
	peerGainFactor float32,
	peerAttenuationExponent float64,
	encoderPos common.Position,
	peerPos common.Position,
	order int,
	size float64) {

	norm, distance := common.TrigCartesianNormRectAndDistance(encoderPos, peerPos)

	var attenuationFactor float32
	if peerAttenuationExponent == 1 {
		attenuationFactor = float32(1 / (distance * size))
	} else {
		attenuationFactor = float32(math.Pow(1/(distance*size), peerAttenuationExponent))
	}

	if attenuationFactor > 1.0 {
		attenuationFactor = 1.0
	}

	nodeGain := peerGainFactor * attenuationFactor

	GetSphericalHarmonicsGained(order, float32(norm.X), float32(norm.Y), float32(norm.Z), nodeGain, weights)
}

func GetWeightsForReverb(dryWeights []float32,
	wetWeights []float32,
	sphericalHarmonics []float32,
	peerGainFactor float32,
	peerAttenuationExponent float64,
	encoderPos common.Position,
	peerPos common.Position,
	order int,
	channels int,
	size float64) {

	norm, distance := common.TrigCartesianNormRectAndDistance(encoderPos, peerPos)

	distanceMeters := distance * size

	var attenuationFactor float32
	if peerAttenuationExponent == 1 {
		attenuationFactor = float32(1 / (distance * size))
	} else {
		attenuationFactor = float32(math.Pow(1/(distanceMeters), peerAttenuationExponent))
	}

	if attenuationFactor > 1.0 {
		attenuationFactor = 1.0
	}

	nodeGain := peerGainFactor * attenuationFactor
	reverbGain := smoothstep(1.0, 8.0, distanceMeters) + 0.05
	//dryGain := math.Sqrt(1 - reverbGain)
	dryGain := 1 - reverbGain
	reverbGain = reverbGain * 0.9

	GetSphericalHarmonics(order, float32(norm.X), float32(norm.Y), float32(norm.Z), sphericalHarmonics)

	//only fill in the first 4 channels for reverb
	for i := 0; i < common.REVERB_CHANNELS; i++ {
		dryWeights[i] = sphericalHarmonics[i] * nodeGain * float32(dryGain)
		wetWeights[i] = sphericalHarmonics[i] * nodeGain * float32(reverbGain)
	}

	for i := common.REVERB_CHANNELS; i < channels; i++ {
		dryWeights[i] = sphericalHarmonics[i] * nodeGain
	}
}

func smoothstep(edge0, edge1, x float64) float64 {
	t := (x - edge0) / (edge1 - edge0)

	if t < 0 {
		t = 0
	} else if t > 1 {
		t = 1
	}

	return t * t * (3 - 2*t)
}

func GetWeightsSaf(weights []float32,
	peerGain float64,
	attenuation float64,
	encoderPos common.Position,
	peerPos common.Position,
	order int,
	size float64) {

	relative := common.TrigCartesianRelativePosition(encoderPos, peerPos)
	polar := common.TrigCartesianToPolar(relative)

	nodeGain := float32(math.Min(math.Pow(peerGain/(polar.Distance*size), attenuation), 1.0))

	spacer.Panaudia_utils_getGainedWeights(order, float32(polar.Azimuth), float32(polar.Elevation), nodeGain, uintptr(unsafe.Pointer(&weights[0])))
}

// Constants moved outside function to avoid recreating them
const (
	// Order 0
	SH_0_0 = 0.2820947917738781

	// Order 1
	SH_1_COEF = 0.48860251190292
	SH_1_0    = 0.4886025119029199

	// Order 2
	SH_2_COEF   = 0.5462742152960395
	SH_2_1_COEF = 1.092548430592079
	SH_2_0_A    = 0.9461746957575601
	SH_2_0_B    = 0.31539156525252

	// Order 3
	SH_3_COEF_F   = 0.5900435899266435
	SH_3_COEF_E   = 1.445305721320277
	SH_3_COEF_D_A = 2.285228997322329
	SH_3_COEF_D_B = 0.4570457994644658
	SH_3_0_A      = 1.865881662950577
	SH_3_0_B      = 1.119528997770346

	// Order 4
	SH_4_COEF     = 0.6258357354491763
	SH_4_1_COEF   = 1.770130769779931
	SH_4_2_COEF_A = 3.31161143515146
	SH_4_2_COEF_B = 0.47308734787878
	SH_4_3_COEF_A = 4.683325804901025
	SH_4_3_COEF_B = 2.007139630671868
	SH_4_0_A      = 1.984313483298443
	SH_4_0_B      = 1.006230589874905

	// Order 5
	SH_5_COEF     = 0.6563820568401703
	SH_5_1_COEF   = 2.075662314881041
	SH_5_4_COEF_A = 1.98997487421324
	SH_5_4_COEF_B = 1.002853072844814
	SH_5_3_TMP_A  = 2.03100960115899
	SH_5_3_TMP_B  = 0.991031208965115
	SH_5_2_TMP_A  = 7.190305177459987
	SH_5_2_TMP_B  = 2.396768392486662
	SH_5_1_TMP_A  = 4.403144694917254
	SH_5_1_TMP_B  = 0.4892382994352505
)

//func GetSphericalHarmonicsGained(order int, nx, ny, nz, gain float32, weights []float32) {
//
//	if order == 4 {
//		GetSphericalHarmonics4Gained(nx, ny, nz, gain, weights)
//		return
//	}
//
//	if order == 5 {
//		GetSphericalHarmonics5Gained(nx, ny, nz, gain, weights)
//		return
//	}
//
//	// Precompute commonly used values
//	nx2 := nx * nx
//	ny2 := ny * ny
//	nz2 := nz * nz
//
//	// Order 0 (omnidirectional)
//	weights[0] = SH_0_0 * gain
//
//	// Order 1 (dipole patterns)
//	gain1 := SH_1_COEF * gain
//	weights[1] = gain1 * ny
//	weights[2] = SH_1_0 * nz * gain
//	weights[3] = gain1 * nx
//
//	// Order 2 (quadrupole patterns)
//	fC1 := nx2 - ny2
//	fS1 := 2.0 * nx * ny
//
//	gain2Coef := SH_2_COEF * gain
//	gain21CoefNz := SH_2_1_COEF * nz * gain
//
//	weights[4] = gain2Coef * fS1
//	weights[5] = gain21CoefNz * ny
//	weights[6] = (nz2*SH_2_0_A - SH_2_0_B) * gain
//	weights[7] = gain21CoefNz * nx
//	weights[8] = gain2Coef * fC1
//
//	// Order 3 (octupole patterns)
//	if order > 2 {
//		fTmpD := nz2*SH_3_COEF_D_A - SH_3_COEF_D_B
//		fTmpE := SH_3_COEF_E * nz
//		gain3CoefF := SH_3_COEF_F * gain
//		gain3TmpE := fTmpE * gain
//		gain3TmpD := fTmpD * gain
//
//		// Y(3,-3): involves (x*sin(2φ) + y*cos(2φ)) = x*fS1 + y*fC1
//		weights[9] = gain3CoefF * (nx*fS1 + ny*fC1)
//		// Y(3,-2): z * sin(2φ)
//		weights[10] = gain3TmpE * fS1
//		// Y(3,-1): y * (polynomial in z²)
//		weights[11] = gain3TmpD * ny
//		// Y(3,0): z * (polynomial in z²)
//		weights[12] = nz * (nz2*SH_3_0_A - SH_3_0_B) * gain
//		// Y(3,1): x * (polynomial in z²)
//		weights[13] = gain3TmpD * nx
//		// Y(3,2): z * cos(2φ)
//		weights[14] = gain3TmpE * fC1
//		// Y(3,3): involves (x*cos(2φ) - y*sin(2φ)) = x*fC1 - y*fS1
//		weights[15] = gain3CoefF * (nx*fC1 - ny*fS1)
//	}
//}

func GetSphericalHarmonicsGained(order int, nx, ny, nz, gain float32, weights []float32) {
	GetSphericalHarmonics(order, nx, ny, nz, weights)
	for i := 0; i < len(weights); i++ {
		weights[i] *= gain
	}
}

func GetSphericalHarmonics(order int, nx, ny, nz float32, weights []float32) {

	if order == 4 {
		GetSphericalHarmonics4(nx, ny, nz, weights)
		return
	}

	if order == 5 {
		GetSphericalHarmonics5(nx, ny, nz, weights)
		return
	}

	// Precompute commonly used values
	nx2 := nx * nx
	ny2 := ny * ny
	nz2 := nz * nz

	// Order 0 (omnidirectional)
	weights[0] = SH_0_0

	// Order 1 (dipole patterns)
	//gain1 := SH_1_COEF
	weights[1] = SH_1_COEF * ny
	weights[2] = SH_1_0 * nz
	weights[3] = SH_1_COEF * nx

	// Order 2 (quadrupole patterns)
	fC1 := nx2 - ny2
	fS1 := 2.0 * nx * ny

	Coef21Nz := SH_2_1_COEF * nz

	weights[4] = SH_2_COEF * fS1
	weights[5] = Coef21Nz * ny
	weights[6] = (nz2*SH_2_0_A - SH_2_0_B)
	weights[7] = Coef21Nz * nx
	weights[8] = SH_2_COEF * fC1

	// Order 3 (octupole patterns)
	if order > 2 {
		fTmpD := nz2*SH_3_COEF_D_A - SH_3_COEF_D_B
		fTmpE := SH_3_COEF_E * nz
		// Y(3,-3): involves (x*sin(2φ) + y*cos(2φ)) = x*fS1 + y*fC1
		weights[9] = SH_3_COEF_F * (nx*fS1 + ny*fC1)
		// Y(3,-2): z * sin(2φ)
		weights[10] = fTmpE * fS1
		// Y(3,-1): y * (polynomial in z²)
		weights[11] = fTmpD * ny
		// Y(3,0): z * (polynomial in z²)
		weights[12] = nz * (nz2*SH_3_0_A - SH_3_0_B)
		// Y(3,1): x * (polynomial in z²)
		weights[13] = fTmpD * nx
		// Y(3,2): z * cos(2φ)
		weights[14] = fTmpE * fC1
		// Y(3,3): involves (x*cos(2φ) - y*sin(2φ)) = x*fC1 - y*fS1
		weights[15] = SH_3_COEF_F * (nx*fC1 - ny*fS1)
	}
}

//
//// SHEval4 calculates spherical harmonics of order 4
//func GetSphericalHarmonics4Gained(fX, fY, fZ, gain float32, pSH []float32) {
//	var fC0, fC1, fS0, fS1 float32
//	fZ2 := fZ * fZ
//
//	pSH[0] = SH_0_0 * gain
//	pSH[2] = SH_1_0 * fZ * gain
//
//	temp6 := SH_2_0_A*fZ2 - SH_2_0_B
//	temp12 := fZ * (SH_3_0_A*fZ2 - SH_3_0_B)
//	pSH[6] = temp6 * gain
//	pSH[12] = temp12 * gain
//	pSH[20] = (SH_4_0_A*fZ*temp12 - SH_4_0_B*temp6) * gain
//
//	fC0 = fX
//	fS0 = fY
//
//	// Order 1 terms
//	pSH[3] = SH_1_COEF * fC0 * gain
//	pSH[1] = SH_1_COEF * fS0 * gain
//
//	// Order 2 terms with Z
//	gainZ := SH_2_1_COEF * fZ * gain
//	pSH[7] = gainZ * fC0
//	pSH[5] = gainZ * fS0
//
//	// Order 3 polynomial in Z
//	gainZ2 := (SH_3_COEF_D_A*fZ2 - SH_3_COEF_D_B) * gain
//	pSH[13] = gainZ2 * fC0
//	pSH[11] = gainZ2 * fS0
//
//	// Order 4 polynomial in Z
//	gainZ3 := fZ * (SH_4_3_COEF_A*fZ2 - SH_4_3_COEF_B) * gain
//	pSH[21] = gainZ3 * fC0
//	pSH[19] = gainZ3 * fS0
//
//	fC1 = fX*fC0 - fY*fS0
//	fS1 = fX*fS0 + fY*fC0
//
//	// Order 2 terms
//	pSH[8] = SH_2_COEF * fC1 * gain
//	pSH[4] = SH_2_COEF * fS1 * gain
//
//	// Order 3 terms with Z
//	gainZ = SH_3_COEF_E * fZ * gain
//	pSH[14] = gainZ * fC1
//	pSH[10] = gainZ * fS1
//
//	// Order 4 polynomial in Z
//	gainZ2 = (SH_4_2_COEF_A*fZ2 - SH_4_2_COEF_B) * gain
//	pSH[22] = gainZ2 * fC1
//	pSH[18] = gainZ2 * fS1
//
//	fC0 = fX*fC1 - fY*fS1
//	fS0 = fX*fS1 + fY*fC1
//
//	// Order 3 terms
//	pSH[15] = SH_3_COEF_F * fC0 * gain
//	pSH[9] = SH_3_COEF_F * fS0 * gain
//
//	// Order 4 terms with Z
//	gainZ = SH_4_1_COEF * fZ * gain
//	pSH[23] = gainZ * fC0
//	pSH[17] = gainZ * fS0
//
//	fC1 = fX*fC0 - fY*fS0
//	fS1 = fX*fS0 + fY*fC0
//
//	// Final Order 4 terms
//	pSH[24] = SH_4_COEF * fC1 * gain
//	pSH[16] = SH_4_COEF * fS1 * gain
//}

//// SHEval5 calculates spherical harmonics of order 5
//func GetSphericalHarmonics5Gained(fX, fY, fZ, gain float32, pSH []float32) {
//	var fC0, fC1, fS0, fS1, fTmpA, fTmpB, fTmpC float32
//	fZ2 := fZ * fZ
//
//	pSH[0] = SH_0_0 * gain
//	pSH[2] = SH_1_0 * fZ * gain
//
//	temp6 := SH_2_0_A*fZ2 + -SH_2_0_B
//	temp12 := fZ * (SH_3_0_A*fZ2 + -SH_3_0_B)
//	pSH[6] = temp6 * gain
//	pSH[12] = temp12 * gain
//
//	temp20 := SH_4_0_A*fZ*temp12 + -SH_4_0_B*temp6
//	pSH[20] = temp20 * gain
//	pSH[30] = (SH_5_4_COEF_A*fZ*temp20 + -SH_5_4_COEF_B*temp12) * gain
//	fC0 = fX
//	fS0 = fY
//
//	pSH[3] = SH_1_COEF * fC0 * gain
//	pSH[1] = SH_1_COEF * fS0 * gain
//	fTmpB = SH_2_1_COEF * fZ
//	pSH[7] = fTmpB * fC0 * gain
//	pSH[5] = fTmpB * fS0 * gain
//	fTmpC = SH_3_COEF_D_A*fZ2 + -SH_3_COEF_D_B
//	pSH[13] = fTmpC * fC0 * gain
//	pSH[11] = fTmpC * fS0 * gain
//	fTmpA = fZ * (SH_4_3_COEF_A*fZ2 + -SH_4_3_COEF_B)
//	pSH[21] = fTmpA * fC0 * gain
//	pSH[19] = fTmpA * fS0 * gain
//	fTmpB = SH_5_3_TMP_A*fZ*fTmpA + -SH_5_3_TMP_B*fTmpC
//	pSH[31] = fTmpB * fC0 * gain
//	pSH[29] = fTmpB * fS0 * gain
//	fC1 = fX*fC0 - fY*fS0
//	fS1 = fX*fS0 + fY*fC0
//
//	pSH[8] = SH_2_COEF * fC1 * gain
//	pSH[4] = SH_2_COEF * fS1 * gain
//	fTmpB = SH_3_COEF_E * fZ
//	pSH[14] = fTmpB * fC1 * gain
//	pSH[10] = fTmpB * fS1 * gain
//	fTmpC = SH_4_2_COEF_A*fZ2 + -SH_4_2_COEF_B
//	pSH[22] = fTmpC * fC1 * gain
//	pSH[18] = fTmpC * fS1 * gain
//	fTmpA = fZ * (SH_5_2_TMP_A*fZ2 + -SH_5_2_TMP_B)
//	pSH[32] = fTmpA * fC1 * gain
//	pSH[28] = fTmpA * fS1 * gain
//	fC0 = fX*fC1 - fY*fS1
//	fS0 = fX*fS1 + fY*fC1
//
//	pSH[15] = SH_3_COEF_F * fC0 * gain
//	pSH[9] = SH_3_COEF_F * fS0 * gain
//	fTmpB = SH_4_1_COEF * fZ
//	pSH[23] = fTmpB * fC0 * gain
//	pSH[17] = fTmpB * fS0 * gain
//	fTmpC = SH_5_1_TMP_A*fZ2 + -SH_5_1_TMP_B
//	pSH[33] = fTmpC * fC0 * gain
//	pSH[27] = fTmpC * fS0 * gain
//	fC1 = fX*fC0 - fY*fS0
//	fS1 = fX*fS0 + fY*fC0
//
//	pSH[24] = SH_4_COEF * fC1 * gain
//	pSH[16] = SH_4_COEF * fS1 * gain
//
//	fTmpB = SH_5_1_COEF * fZ
//	pSH[34] = fTmpB * fC1 * gain
//	pSH[26] = fTmpB * fS1 * gain
//	fC0 = fX*fC1 - fY*fS1
//	fS0 = fX*fS1 + fY*fC1
//
//	pSH[35] = SH_5_COEF * fC0 * gain
//	pSH[25] = SH_5_COEF * fS0 * gain
//}

// SHEval4 calculates spherical harmonics of order 4
func GetSphericalHarmonics4(fX, fY, fZ float32, pSH []float32) {
	var fC0, fC1, fS0, fS1 float32
	fZ2 := fZ * fZ

	pSH[0] = SH_0_0
	pSH[2] = SH_1_0 * fZ

	temp6 := SH_2_0_A*fZ2 - SH_2_0_B
	temp12 := fZ * (SH_3_0_A*fZ2 - SH_3_0_B)
	pSH[6] = temp6
	pSH[12] = temp12
	pSH[20] = (SH_4_0_A*fZ*temp12 - SH_4_0_B*temp6)

	fC0 = fX
	fS0 = fY

	// Order 1 terms
	pSH[3] = SH_1_COEF * fC0
	pSH[1] = SH_1_COEF * fS0

	// Order 2 terms with Z
	gainZ := SH_2_1_COEF * fZ
	pSH[7] = gainZ * fC0
	pSH[5] = gainZ * fS0

	// Order 3 polynomial in Z
	gainZ2 := (SH_3_COEF_D_A*fZ2 - SH_3_COEF_D_B)
	pSH[13] = gainZ2 * fC0
	pSH[11] = gainZ2 * fS0

	// Order 4 polynomial in Z
	gainZ3 := fZ * (SH_4_3_COEF_A*fZ2 - SH_4_3_COEF_B)
	pSH[21] = gainZ3 * fC0
	pSH[19] = gainZ3 * fS0

	fC1 = fX*fC0 - fY*fS0
	fS1 = fX*fS0 + fY*fC0

	// Order 2 terms
	pSH[8] = SH_2_COEF * fC1
	pSH[4] = SH_2_COEF * fS1

	// Order 3 terms with Z
	gainZ = SH_3_COEF_E * fZ
	pSH[14] = gainZ * fC1
	pSH[10] = gainZ * fS1

	// Order 4 polynomial in Z
	gainZ2 = (SH_4_2_COEF_A*fZ2 - SH_4_2_COEF_B)
	pSH[22] = gainZ2 * fC1
	pSH[18] = gainZ2 * fS1

	fC0 = fX*fC1 - fY*fS1
	fS0 = fX*fS1 + fY*fC1

	// Order 3 terms
	pSH[15] = SH_3_COEF_F * fC0
	pSH[9] = SH_3_COEF_F * fS0

	// Order 4 terms with Z
	gainZ = SH_4_1_COEF * fZ
	pSH[23] = gainZ * fC0
	pSH[17] = gainZ * fS0

	fC1 = fX*fC0 - fY*fS0
	fS1 = fX*fS0 + fY*fC0

	// Final Order 4 terms
	pSH[24] = SH_4_COEF * fC1
	pSH[16] = SH_4_COEF * fS1
}

// SHEval5 calculates spherical harmonics of order 5
func GetSphericalHarmonics5(fX, fY, fZ float32, pSH []float32) {
	var fC0, fC1, fS0, fS1, fTmpA, fTmpB, fTmpC float32
	fZ2 := fZ * fZ

	pSH[0] = SH_0_0
	pSH[2] = SH_1_0 * fZ

	temp6 := SH_2_0_A*fZ2 + -SH_2_0_B
	temp12 := fZ * (SH_3_0_A*fZ2 + -SH_3_0_B)
	pSH[6] = temp6
	pSH[12] = temp12

	temp20 := SH_4_0_A*fZ*temp12 + -SH_4_0_B*temp6
	pSH[20] = temp20
	pSH[30] = (SH_5_4_COEF_A*fZ*temp20 + -SH_5_4_COEF_B*temp12)
	fC0 = fX
	fS0 = fY

	pSH[3] = SH_1_COEF * fC0
	pSH[1] = SH_1_COEF * fS0
	fTmpB = SH_2_1_COEF * fZ
	pSH[7] = fTmpB * fC0
	pSH[5] = fTmpB * fS0
	fTmpC = SH_3_COEF_D_A*fZ2 + -SH_3_COEF_D_B
	pSH[13] = fTmpC * fC0
	pSH[11] = fTmpC * fS0
	fTmpA = fZ * (SH_4_3_COEF_A*fZ2 + -SH_4_3_COEF_B)
	pSH[21] = fTmpA * fC0
	pSH[19] = fTmpA * fS0
	fTmpB = SH_5_3_TMP_A*fZ*fTmpA + -SH_5_3_TMP_B*fTmpC
	pSH[31] = fTmpB * fC0
	pSH[29] = fTmpB * fS0
	fC1 = fX*fC0 - fY*fS0
	fS1 = fX*fS0 + fY*fC0

	pSH[8] = SH_2_COEF * fC1
	pSH[4] = SH_2_COEF * fS1
	fTmpB = SH_3_COEF_E * fZ
	pSH[14] = fTmpB * fC1
	pSH[10] = fTmpB * fS1
	fTmpC = SH_4_2_COEF_A*fZ2 + -SH_4_2_COEF_B
	pSH[22] = fTmpC * fC1
	pSH[18] = fTmpC * fS1
	fTmpA = fZ * (SH_5_2_TMP_A*fZ2 + -SH_5_2_TMP_B)
	pSH[32] = fTmpA * fC1
	pSH[28] = fTmpA * fS1
	fC0 = fX*fC1 - fY*fS1
	fS0 = fX*fS1 + fY*fC1

	pSH[15] = SH_3_COEF_F * fC0
	pSH[9] = SH_3_COEF_F * fS0
	fTmpB = SH_4_1_COEF * fZ
	pSH[23] = fTmpB * fC0
	pSH[17] = fTmpB * fS0
	fTmpC = SH_5_1_TMP_A*fZ2 + -SH_5_1_TMP_B
	pSH[33] = fTmpC * fC0
	pSH[27] = fTmpC * fS0
	fC1 = fX*fC0 - fY*fS0
	fS1 = fX*fS0 + fY*fC0

	pSH[24] = SH_4_COEF * fC1
	pSH[16] = SH_4_COEF * fS1

	fTmpB = SH_5_1_COEF * fZ
	pSH[34] = fTmpB * fC1
	pSH[26] = fTmpB * fS1
	fC0 = fX*fC1 - fY*fS1
	fS0 = fX*fS1 + fY*fC1

	pSH[35] = SH_5_COEF * fC0
	pSH[25] = SH_5_COEF * fS0
}
