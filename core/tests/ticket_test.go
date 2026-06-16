package tests

import (
	"fmt"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/panaudia/panaudia/core/common"
	"github.com/panaudia/panaudia/direct"
	"os"
	"testing"
	"time"
)

func TestATicket(t *testing.T) {

	priv, _ := os.ReadFile("private.pem")
	privateKey, _ := jwt.ParseEdPrivateKeyFromPEM(priv)

	node_config := common.J{
		"attenuation": 2.5,
		"gain":        1.5,
		"priority":    true,
		"attrs": common.J{
			"colour": "ff0000",
		},
	}

	uid := uuid.New()

	token := jwt.NewWithClaims(jwt.SigningMethodEdDSA, jwt.MapClaims{
		"iss":                "testing",
		"iat":                time.Now().Unix(),
		"preferred_username": "paul",
		"jti":                uid.String(),
		"aud":                "space_8eaf1244-098b-41f1-b0c9-734ea0901a93",
		"panaudia":           node_config})

	// Sign and get the complete encoded token as a string using the secret
	tokenString, _ := token.SignedString(privateKey)

	auth := direct.NewDirectAuthoriser("public.pem", 2, nil, nil)

	q := map[string][]string{"ticket": {tokenString}}

	conf, _ := auth.Authorise(q)

	////fmt.Printf("err: %v", err)
	fmt.Printf("conf: %v", conf)

	if conf.Name != "paul" {
		t.Fatalf(`name not equal - got: %v expected: %v`, conf.Name, "paul")
	}

	if conf.Uuid != uid {
		t.Fatalf(`uuid not equal - got: %v expected: %v`, conf.Uuid, uid)
	}

	if conf.Uuid != conf.Uuid {
		t.Fatalf(`uuid not equal - got: %v expected: %v`, conf.Uuid, conf.Uuid)
	}

	if conf.Priority != true {
		t.Fatalf(`Priority not equal - got: %v expected: %v`, conf.Priority, true)
	}

	if conf.NullOut != false {
		t.Fatalf(`NullOut not equal - got: %v expected: %v`, conf.NullOut, false)
	}

	if conf.Input != true {
		t.Fatalf(`Input not equal - got: %v expected: %v`, conf.Input, true)
	}

	if conf.Gain != 1.5 {
		t.Fatalf(`Gain not equal - got: %v expected: %v`, conf.Gain, 1.5)
	}

	if conf.Attenuation != 2.5 {
		t.Fatalf(`Attenuation not equal - got: %v expected: %v`, conf.Attenuation, 2.5)
	}

	if conf.Tone != 0.0 {
		t.Fatalf(`Tone not equal - got: %v expected: %v`, conf.Tone, 0.0)
	}
}

func TestATicketWithExtras(t *testing.T) {

	priv, _ := os.ReadFile("private.pem")
	privateKey, _ := jwt.ParseEdPrivateKeyFromPEM(priv)

	node_config := common.J{
		"attenuation": 2.5,
		"gain":        1.5,
		"priority":    true,
		"attrs": common.J{
			"colour": "ff0000",
		},
	}

	uid := uuid.New()

	token := jwt.NewWithClaims(jwt.SigningMethodEdDSA, jwt.MapClaims{
		"iss":                "testing",
		"iat":                time.Now().Unix(),
		"preferred_username": "paul",
		"jti":                uid.String(),
		"aud":                "space_8eaf1244-098b-41f1-b0c9-734ea0901a93",
		"panaudia":           node_config})

	// Sign and get the complete encoded token as a string using the secret
	tokenString, _ := token.SignedString(privateKey)

	auth := direct.NewDirectAuthoriser("public.pem", 2, nil, nil)

	q := map[string][]string{"ticket": {tokenString},
		"x":      {"0.5"},
		"y":      {"0.7"},
		"z":      {"0.6"},
		"yaw":    {"30"},
		"pitch":  {"20"},
		"roll":   {"45"},
		"colour": {"0000ff"},
	}

	conf, _ := auth.Authorise(q)

	////fmt.Printf("err: %v", err)
	fmt.Printf("conf: %v", conf)

	if conf.Name != "paul" {
		t.Fatalf(`name not equal - got: %v expected: %v`, conf.Name, "paul")
	}

	if conf.Uuid != uid {
		t.Fatalf(`uuid not equal - got: %v expected: %v`, conf.Uuid, uid)
	}

	if conf.Priority != true {
		t.Fatalf(`Priority not equal - got: %v expected: %v`, conf.Priority, true)
	}

	if conf.NullOut != false {
		t.Fatalf(`NullOut not equal - got: %v expected: %v`, conf.NullOut, false)
	}

	if conf.Input != true {
		t.Fatalf(`Input not equal - got: %v expected: %v`, conf.Input, true)
	}

	if conf.Gain != 1.5 {
		t.Fatalf(`Gain not equal - got: %v expected: %v`, conf.Gain, 1.5)
	}

	if conf.Attenuation != 2.5 {
		t.Fatalf(`Attenuation not equal - got: %v expected: %v`, conf.Attenuation, 2.5)
	}

	if conf.Tone != 0.0 {
		t.Fatalf(`Tone not equal - got: %v expected: %v`, conf.Tone, 0.0)
	}

	var ticketAttrs = conf.Attrs["ticket"]
	var connectionAttrs = conf.Attrs["connection"]

	if v, ok := ticketAttrs.(common.J); ok {
		if v["colour"] != "ff0000" {
			t.Fatalf(`colour not equal - got: %v expected: %v`, v["colour"], "ff0000")
		}
	} else {
		t.Fatalf("counld read ticketAttrs")
	}

	if v, ok := connectionAttrs.(common.J); ok {
		if v["colour"] != "0000ff" {
			t.Fatalf(`connection colour not equal - got: %v expected: %v`, v["colour"], "0000ff")
		}
	} else {
		t.Fatalf("counld read ticketAttrs")
	}

	position := common.Position{X: 0.5, Y: 0.7, Z: 0.6}
	if conf.Position != position {
		t.Fatalf(`position not equal - got: %v expected: %v`, conf.Position, position)
	}
}

func TestWithoutTicket(t *testing.T) {

	uid := uuid.New()

	auth := direct.NewDirectAuthoriser("", 2, nil, nil)

	q := map[string][]string{"uuid": {uid.String()},
		"name":        {"paul"},
		"x":           {"0.5"},
		"y":           {"0.7"},
		"z":           {"0.6"},
		"yaw":         {"30"},
		"pitch":       {"20"},
		"roll":        {"45"},
		"colour":      {"0000ff"},
		"gain":        {"1.5"},
		"attenuation": {"2.5"},
	}

	conf, _ := auth.AuthoriseWithoutTicket(q)

	////fmt.Printf("err: %v", err)
	fmt.Printf("conf: %v", conf)

	if conf.Name != "paul" {
		t.Fatalf(`name not equal - got: %v expected: %v`, conf.Name, "paul")
	}

	if conf.Uuid != uid {
		t.Fatalf(`uuid not equal - got: %v expected: %v`, conf.Uuid, uid)
	}

	if conf.Priority != false {
		t.Fatalf(`Priority not equal - got: %v expected: %v`, conf.Priority, true)
	}

	if conf.NullOut != false {
		t.Fatalf(`NullOut not equal - got: %v expected: %v`, conf.NullOut, false)
	}

	if conf.Input != true {
		t.Fatalf(`Input not equal - got: %v expected: %v`, conf.Input, true)
	}

	if conf.Gain != 1.5 {
		t.Fatalf(`Gain not equal - got: %v expected: %v`, conf.Gain, 1.5)
	}

	if conf.Attenuation != 2.5 {
		t.Fatalf(`Attenuation not equal - got: %v expected: %v`, conf.Attenuation, 2.5)
	}

	if conf.Tone != 0.0 {
		t.Fatalf(`Tone not equal - got: %v expected: %v`, conf.Tone, 0.0)
	}

	var connectionAttrs = conf.Attrs["connection"]

	if v, ok := connectionAttrs.(common.J); ok {
		if v["colour"] != "0000ff" {
			t.Fatalf(`connection colour not equal - got: %v expected: %v`, v["colour"], "0000ff")
		}
	} else {
		t.Fatalf("counld read ticketAttrs")
	}

	position := common.Position{X: 0.5, Y: 0.7, Z: 0.6}
	if conf.Position != position {
		t.Fatalf(`position not equal - got: %v expected: %v`, conf.Position, position)
	}
}
