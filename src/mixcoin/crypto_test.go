package mixcoin

import (
	"bytes"

	"code.google.com/p/go.crypto/openpgp"
	"code.google.com/p/go.crypto/openpgp/armor"

	"testing"
)

func TestSig(t *testing.T) {
	cfg = GetConfig()
	text := "test text test text"

	entity := getPgpEntity()
	sig := signText(entity, text)
	t.Logf("signature: %s", sig)

	armorWriter := bytes.NewBuffer(nil)
	writer, err := armor.Encode(armorWriter, openpgp.PublicKeyType, nil)
	if err != nil {
		panic(err)
	}

	if err = entity.Serialize(writer); err != nil {
		panic(err)
	}

	if err = writer.Close(); err != nil {
		panic(err)
	}

	armoredPK := armorWriter.String()
	t.Logf("got armored pk from entity:\n%s", armoredPK)

	verified := verifySignature(armoredPK, text, sig)
	if !verified {
		t.Logf("unsuccessful signature verification!")
		t.Fail()
	}
}
