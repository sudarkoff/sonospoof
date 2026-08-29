// Package raop implements the pieces of the AirPlay 1 (RAOP) receiver side
// that a sender requires before it will send us any audio.
package raop

import (
	"crypto/rsa"
	"crypto/x509"
	_ "embed"
	"encoding/pem"
	"fmt"
)

// airportExpressPEM is the RSA private key from the original AirPort Express.
//
// It has been public for well over a decade and ships in-tree in every open
// RAOP receiver -- shairport-sync, AirConnect, forked-daapd. This copy came
// from shairport-sync's common.c.
//
// It is here for one reason: iOS will not send ANNOUNCE to a receiver that
// cannot answer its Apple-Challenge, so without this there is no stream to
// decode. That was established empirically against George's iPhone, not
// assumed -- advertising et=0 to sidestep the challenge made iOS skip the
// challenge and then refuse to proceed past OPTIONS anyway.
//
// Embedded rather than pasted into a string literal so the bytes cannot drift.
//
//go:embed airport_express.pem
var airportExpressPEM []byte

// airportExpressKey is parsed once at init; a malformed key is a build-time
// mistake, not something worth handling at every challenge.
var airportExpressKey = mustParseKey(airportExpressPEM)

func mustParseKey(pemBytes []byte) *rsa.PrivateKey {
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		panic("raop: embedded AirPort Express key is not valid PEM")
	}
	key, err := x509.ParsePKCS1PrivateKey(block.Bytes)
	if err != nil {
		panic(fmt.Sprintf("raop: parsing embedded AirPort Express key: %v", err))
	}
	return key
}
