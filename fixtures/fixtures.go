package fixtures

import (
	"fmt"
	"math"
	"math/rand"
	"time"

	"github.com/google/uuid"
	"github.com/panaudia/panaudia/core/common"
	"github.com/panaudia/panaudia/core/space"
)

func addRandomPerson(iSpace space.ISpace) {
	id := uuid.New()
	s1 := rand.NewSource(time.Now().UnixNano())
	r1 := rand.New(s1)
	tone := semiToneToHzz(r1.Intn(48) - 24)

	config := common.SpaceNodeConfig{Gain: 1.0,
		Attenuation: 3.0,
		Input:       false,
		Tone:        tone,
		Priority:    false,
		NullOut:     true}

	iSpace.AddNodeQStyled(id.String(),
		common.Position{X: r1.Float64(), Y: r1.Float64(), Z: 0.5},
		map[string]string{"null": "true", "tone": fmt.Sprintf("%.2f", tone)},
		config)

}

func addPlayer(iSpace space.ISpace) {

	config := common.SpaceNodeConfig{Gain: 1.0,
		Attenuation: 2.0,
		Input:       false,
		Tone:        0.0,
		Priority:    false,
		NullOut:     false}

	iSpace.AddNodeQStyled("player", common.Position{X: 0.5, Y: 0.5, Z: 0.5}, map[string]string{"player": "true"}, config)
}

func semiToneToHz(semiTones int) string {
	return fmt.Sprintf("%.2f", math.Pow(2.0, (float64(semiTones/12.0)))*440)
}

func semiToneToHzz(semiTones int) float64 {
	return math.Pow(2.0, (float64(semiTones/12.0))) * 440
}

func addRandomInstrument(iSpace space.ISpace) {
	id := uuid.New()
	s1 := rand.NewSource(time.Now().UnixNano())
	r1 := rand.New(s1)
	tone := semiToneToHzz(r1.Intn(48) - 24)

	config := common.SpaceNodeConfig{Gain: 1.0,
		Attenuation: 2.0,
		Input:       false,
		Tone:        tone,
		Priority:    false,
		NullOut:     false}

	iSpace.AddNodeQStyled(id.String(),
		common.Position{X: r1.Float64(), Y: r1.Float64(), Z: r1.Float64() / 8},
		map[string]string{"tone": fmt.Sprintf("%.2f", tone)}, config)
}

func addRandomAudienceMember(iSpace space.ISpace) {
	id := uuid.New()
	s1 := rand.NewSource(time.Now().UnixNano())
	r1 := rand.New(s1)

	config := common.SpaceNodeConfig{Gain: 1.0,
		Attenuation: 2.0,
		Input:       false,
		Tone:        0.0,
		Priority:    false,
		NullOut:     true}

	iSpace.AddNodeQStyled(id.String(),
		common.Position{X: r1.Float64(), Y: r1.Float64(), Z: r1.Float64() / 8},
		map[string]string{"null": "true"}, config)
}

func AddRandomInstruments(iSpace space.ISpace, count int) {
	for i := 0; i < count; i++ {
		addRandomInstrument(iSpace)
	}
}

func AddRandomPeople(c space.ISpace, count int) {
	for i := 0; i < count; i++ {
		addRandomPerson(c)
	}
}

func AddRandomAudience(iSpace space.ISpace, count int) {
	for i := 0; i < count; i++ {
		addRandomAudienceMember(iSpace)
	}
}

func AddTestTone(iSpace space.ISpace) {

	config := common.SpaceNodeConfig{Gain: 1.0,
		Attenuation: 2.0,
		Input:       false,
		Tone:        707,
		Priority:    false,
		NullOut:     false}

	iSpace.AddNodeQStyled("dog", common.Position{X: 0.5, Y: 0.5, Z: 0.5}, map[string]string{"tone": "707"}, config)
}
