package squads

import (
	"encoding/binary"
	"errors"

	"github.com/Overclock-Validator/mithril-agent/solana"
)

// A spending limit describes how much can leave. It says nothing about who can
// take the limit away, and that is the other half of the question: an operator
// who cannot participate in removing a limit has accepted a boundary they do
// not control. Answering "how is this revoked" means reading the Multisig account the limit
// belongs to, because the removal path is decided by its config_authority.

// multisigDiscriminator is sha256("account:Multisig")[:8]. The value was verified by
// recomputing the derivation, which also reproduces the SpendingLimit tag
// already in this package.
var multisigDiscriminator = [8]byte{0xe0, 0x74, 0x79, 0xba, 0x44, 0xa1, 0x4f, 0xec}

// Field offsets up to rent_collector, which is where the layout stops being
// fixed.
//
// Multisig::size() in the program allocates 32 bytes for rent_collector whether
// or not it is set, and its comment says as much — but that is the ALLOCATION,
// not the encoding. Borsh writes Option<Pubkey> as a one-byte tag followed by
// the key only when it is Some, and Anchor leaves the unused allocation as
// trailing zeros. So everything after rent_collector moves by 32 bytes
// depending on that tag, and the account is longer than the data it holds.
//
// Both facts are confirmed against real accounts: a devnet multisig with
// rent_collector None puts bump at 95, and a mainnet one with Some puts it at
// 127. Reading either at a fixed offset misreports who controls the Multisig.
const (
	offsetCreateKeyMS       = 8
	offsetConfigAuthority   = 40
	offsetThreshold         = 72
	offsetTimeLock          = 74
	offsetTransactionIndex  = 78
	offsetStaleTxIndex      = 86
	offsetRentCollectorFlag = 94
	// A Member is a 32-byte key plus a 1-byte permissions bitmask.
	memberBytes    = pubkeyBytes + 1
	maxMultisigMem = 65_536
)

// AutonomousConfigAuthority is the all-zero key Squads uses to mean "no single
// config authority". It is a real, load-bearing value rather than an absence:
// it selects the member-voting path for every config change.
const AutonomousConfigAuthority = "11111111111111111111111111111111"

// Member is one multisig member and the permissions it holds.
type Member struct {
	Key string `json:"key"`
	// Permissions is the raw Squads bitmask: 1 initiate, 2 vote, 4 execute.
	Permissions uint8 `json:"permissions"`
}

const (
	PermissionInitiate uint8 = 1
	PermissionVote     uint8 = 2
	PermissionExecute  uint8 = 4
)

// Multisig is the subset of the account needed to answer who controls the
// configuration and who can change it.
type Multisig struct {
	CreateKey string `json:"create_key"`
	// ConfigAuthority is the key that can change members and threshold without
	// a vote. AutonomousConfigAuthority means no such key exists.
	ConfigAuthority  string   `json:"config_authority"`
	Threshold        uint16   `json:"threshold"`
	TimeLock         uint32   `json:"time_lock_seconds"`
	TransactionIndex uint64   `json:"transaction_index"`
	StaleTxIndex     uint64   `json:"stale_transaction_index"`
	RentCollector    string   `json:"rent_collector,omitempty"`
	Bump             uint8    `json:"bump"`
	Members          []Member `json:"members"`
}

// Autonomous reports whether config changes go through member voting rather
// than through a single authority.
func (m Multisig) Autonomous() bool {
	return m.ConfigAuthority == AutonomousConfigAuthority
}

// DecodeMultisig reads the account. Like the spending-limit decoder it is
// strict about length everywhere: a truncated account that happened to decode
// would misreport who controls the vault, which is worse than failing.
func DecodeMultisig(data []byte) (Multisig, error) {
	if len(data) < offsetRentCollectorFlag+1 {
		return Multisig{}, errors.New("multisig account is too short")
	}
	if [8]byte(data[:8]) != multisigDiscriminator {
		return Multisig{}, errors.New("account is not a Squads multisig")
	}

	// Walk past rent_collector rather than assuming its width.
	cursor := offsetRentCollectorFlag
	rentCollectorSet := data[cursor] == 1
	switch data[cursor] {
	case 0:
		cursor++
	case 1:
		cursor += 1 + pubkeyBytes
	default:
		return Multisig{}, errors.New("multisig rent collector option tag is not 0 or 1")
	}
	if cursor+1+4 > len(data) {
		return Multisig{}, errors.New("multisig account ends before the member list")
	}
	bump := data[cursor]
	cursor++

	count := binary.LittleEndian.Uint32(data[cursor : cursor+4])
	if count > maxMultisigMem {
		return Multisig{}, errors.New("multisig lists an implausible number of members")
	}
	membersStart := cursor + 4
	end := membersStart + int(count)*memberBytes
	if end > len(data) {
		return Multisig{}, errors.New("multisig account ends inside the member list")
	}
	// Anchor allocates more than it writes and reallocs leave the slack behind,
	// so trailing bytes are expected — but they must be the zero padding that
	// implies, never unread content that would mean the layout was misread.
	for _, b := range data[end:] {
		if b != 0 {
			return Multisig{}, errors.New("multisig account has non-zero trailing bytes")
		}
	}

	multisig := Multisig{
		CreateKey:        solana.Encode(data[offsetCreateKeyMS : offsetCreateKeyMS+pubkeyBytes]),
		ConfigAuthority:  solana.Encode(data[offsetConfigAuthority : offsetConfigAuthority+pubkeyBytes]),
		Threshold:        binary.LittleEndian.Uint16(data[offsetThreshold : offsetThreshold+2]),
		TimeLock:         binary.LittleEndian.Uint32(data[offsetTimeLock : offsetTimeLock+4]),
		TransactionIndex: binary.LittleEndian.Uint64(data[offsetTransactionIndex : offsetTransactionIndex+8]),
		StaleTxIndex:     binary.LittleEndian.Uint64(data[offsetStaleTxIndex : offsetStaleTxIndex+8]),
		Bump:             bump,
	}
	if rentCollectorSet {
		start := offsetRentCollectorFlag + 1
		multisig.RentCollector = solana.Encode(data[start : start+pubkeyBytes])
	}
	members := make([]Member, 0, count)
	for index := range int(count) {
		start := membersStart + index*memberBytes
		members = append(members, Member{
			Key:         solana.Encode(data[start : start+pubkeyBytes]),
			Permissions: data[start+pubkeyBytes],
		})
	}
	if len(members) == 0 {
		return Multisig{}, errors.New("multisig has no members")
	}
	multisig.Members = members
	return multisig, nil
}
