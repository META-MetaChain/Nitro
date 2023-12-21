// Copyright 2021-2022, Offchain Labs, Inc.
// For license information, see https://github.com/nitro/blob/master/LICENSE

package METAosState

import (
	"errors"
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/math"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/params"

	"github.com/META-MetaChain/nitro/METAcompress"
	"github.com/META-MetaChain/nitro/METAos/addressSet"
	"github.com/META-MetaChain/nitro/METAos/addressTable"
	"github.com/META-MetaChain/nitro/METAos/METAostypes"
	"github.com/META-MetaChain/nitro/METAos/blockhash"
	"github.com/META-MetaChain/nitro/METAos/burn"
	"github.com/META-MetaChain/nitro/METAos/l1pricing"
	"github.com/META-MetaChain/nitro/METAos/l2pricing"
	"github.com/META-MetaChain/nitro/METAos/merkleAccumulator"
	"github.com/META-MetaChain/nitro/METAos/retryables"
	"github.com/META-MetaChain/nitro/METAos/storage"
	"github.com/META-MetaChain/nitro/METAos/util"
)

// METAosState contains METAOS-related state. It is backed by METAOS's storage in the persistent stateDB.
// Modifications to the METAosState are written through to the underlying StateDB so that the StateDB always
// has the definitive state, stored persistently. (Note that some tests use memory-backed StateDB's that aren't
// persisted beyond the end of the test.)

type METAosState struct {
	METAosVersion           uint64                      // version of the METAOS storage format and semantics
	upgradeVersion         storage.StorageBackedUint64 // version we're planning to upgrade to, or 0 if not planning to upgrade
	upgradeTimestamp       storage.StorageBackedUint64 // when to do the planned upgrade
	networkFeeAccount      storage.StorageBackedAddress
	l1PricingState         *l1pricing.L1PricingState
	l2PricingState         *l2pricing.L2PricingState
	retryableState         *retryables.RetryableState
	addressTable           *addressTable.AddressTable
	chainOwners            *addressSet.AddressSet
	sendMerkle             *merkleAccumulator.MerkleAccumulator
	blockhashes            *blockhash.Blockhashes
	chainId                storage.StorageBackedBigInt
	chainConfig            storage.StorageBackedBytes
	genesisBlockNum        storage.StorageBackedUint64
	infraFeeAccount        storage.StorageBackedAddress
	brotliCompressionLevel storage.StorageBackedUint64 // brotli compression level used for pricing
	backingStorage         *storage.Storage
	Burner                 burn.Burner
}

var ErrUninitializedMETAOS = errors.New("METAOS uninitialized")
var ErrAlreadyInitialized = errors.New("METAOS is already initialized")

func OpenMETAosState(stateDB vm.StateDB, burner burn.Burner) (*METAosState, error) {
	backingStorage := storage.NewGeth(stateDB, burner)
	METAosVersion, err := backingStorage.GetUint64ByUint64(uint64(versionOffset))
	if err != nil {
		return nil, err
	}
	if METAosVersion == 0 {
		return nil, ErrUninitializedMETAOS
	}
	return &METAosState{
		METAosVersion,
		backingStorage.OpenStorageBackedUint64(uint64(upgradeVersionOffset)),
		backingStorage.OpenStorageBackedUint64(uint64(upgradeTimestampOffset)),
		backingStorage.OpenStorageBackedAddress(uint64(networkFeeAccountOffset)),
		l1pricing.OpenL1PricingState(backingStorage.OpenCachedSubStorage(l1PricingSubspace)),
		l2pricing.OpenL2PricingState(backingStorage.OpenCachedSubStorage(l2PricingSubspace)),
		retryables.OpenRetryableState(backingStorage.OpenCachedSubStorage(retryablesSubspace), stateDB),
		addressTable.Open(backingStorage.OpenCachedSubStorage(addressTableSubspace)),
		addressSet.OpenAddressSet(backingStorage.OpenCachedSubStorage(chainOwnerSubspace)),
		merkleAccumulator.OpenMerkleAccumulator(backingStorage.OpenCachedSubStorage(sendMerkleSubspace)),
		blockhash.OpenBlockhashes(backingStorage.OpenCachedSubStorage(blockhashesSubspace)),
		backingStorage.OpenStorageBackedBigInt(uint64(chainIdOffset)),
		backingStorage.OpenStorageBackedBytes(chainConfigSubspace),
		backingStorage.OpenStorageBackedUint64(uint64(genesisBlockNumOffset)),
		backingStorage.OpenStorageBackedAddress(uint64(infraFeeAccountOffset)),
		backingStorage.OpenStorageBackedUint64(uint64(brotliCompressionLevelOffset)),
		backingStorage,
		burner,
	}, nil
}

func OpenSystemMETAosState(stateDB vm.StateDB, tracingInfo *util.TracingInfo, readOnly bool) (*METAosState, error) {
	burner := burn.NewSystemBurner(tracingInfo, readOnly)
	newState, err := OpenMETAosState(stateDB, burner)
	burner.Restrict(err)
	return newState, err
}

func OpenSystemMETAosStateOrPanic(stateDB vm.StateDB, tracingInfo *util.TracingInfo, readOnly bool) *METAosState {
	newState, err := OpenSystemMETAosState(stateDB, tracingInfo, readOnly)
	if err != nil {
		panic(err)
	}
	return newState
}

// NewMETAosMemoryBackedMETAOSState creates and initializes a memory-backed METAOS state (for testing only)
func NewMETAosMemoryBackedMETAOSState() (*METAosState, *state.StateDB) {
	raw := rawdb.NewMemoryDatabase()
	db := state.NewDatabase(raw)
	statedb, err := state.New(common.Hash{}, db, nil)
	if err != nil {
		log.Crit("failed to init empty statedb", "error", err)
	}
	burner := burn.NewSystemBurner(nil, false)
	chainConfig := params.metachainDevTestChainConfig()
	newState, err := InitializeMETAosState(statedb, burner, chainConfig, METAostypes.TestInitMessage)
	if err != nil {
		log.Crit("failed to open the METAOS state", "error", err)
	}
	return newState, statedb
}

// METAOSVersion returns the METAOS version
func METAOSVersion(stateDB vm.StateDB) uint64 {
	backingStorage := storage.NewGeth(stateDB, burn.NewSystemBurner(nil, false))
	METAosVersion, err := backingStorage.GetUint64ByUint64(uint64(versionOffset))
	if err != nil {
		log.Crit("failed to get the METAOS version", "error", err)
	}
	return METAosVersion
}

type Offset uint64

const (
	versionOffset Offset = iota
	upgradeVersionOffset
	upgradeTimestampOffset
	networkFeeAccountOffset
	chainIdOffset
	genesisBlockNumOffset
	infraFeeAccountOffset
	brotliCompressionLevelOffset
)

type SubspaceID []byte

var (
	l1PricingSubspace    SubspaceID = []byte{0}
	l2PricingSubspace    SubspaceID = []byte{1}
	retryablesSubspace   SubspaceID = []byte{2}
	addressTableSubspace SubspaceID = []byte{3}
	chainOwnerSubspace   SubspaceID = []byte{4}
	sendMerkleSubspace   SubspaceID = []byte{5}
	blockhashesSubspace  SubspaceID = []byte{6}
	chainConfigSubspace  SubspaceID = []byte{7}
)

// Returns a list of precompiles that only appear in metachain chains (i.e. METAOS precompiles) at the genesis block
func getmetachainOnlyGenesisPrecompiles(chainConfig *params.ChainConfig) []common.Address {
	rules := chainConfig.Rules(big.NewInt(0), false, 0, chainConfig.metachainChainParams.InitialMETAOSVersion)
	METAPrecompiles := vm.ActivePrecompiles(rules)
	rules.Ismetachain = false
	ethPrecompiles := vm.ActivePrecompiles(rules)

	ethPrecompilesSet := make(map[common.Address]bool)
	for _, addr := range ethPrecompiles {
		ethPrecompilesSet[addr] = true
	}

	var METAOnlyPrecompiles []common.Address
	for _, addr := range METAPrecompiles {
		if !ethPrecompilesSet[addr] {
			METAOnlyPrecompiles = append(METAOnlyPrecompiles, addr)
		}
	}
	return METAOnlyPrecompiles
}

// During early development we sometimes change the storage format of version 1, for convenience. But as soon as we
// start running long-lived chains, every change to the storage format will require defining a new version and
// providing upgrade code.

func InitializeMETAosState(stateDB vm.StateDB, burner burn.Burner, chainConfig *params.ChainConfig, initMessage *METAostypes.ParsedInitMessage) (*METAosState, error) {
	sto := storage.NewGeth(stateDB, burner)
	METAosVersion, err := sto.GetUint64ByUint64(uint64(versionOffset))
	if err != nil {
		return nil, err
	}
	if METAosVersion != 0 {
		return nil, ErrAlreadyInitialized
	}

	desiredMETAosVersion := chainConfig.metachainChainParams.InitialMETAOSVersion
	if desiredMETAosVersion == 0 {
		return nil, errors.New("cannot initialize to METAOS version 0")
	}

	// Solidity requires call targets have code, but precompiles don't.
	// To work around this, we give precompiles fake code.
	for _, genesisPrecompile := range getmetachainOnlyGenesisPrecompiles(chainConfig) {
		stateDB.SetCode(genesisPrecompile, []byte{byte(vm.INVALID)})
	}

	// may be the zero address
	initialChainOwner := chainConfig.metachainChainParams.InitialChainOwner

	_ = sto.SetUint64ByUint64(uint64(versionOffset), 1) // initialize to version 1; upgrade at end of this func if needed
	_ = sto.SetUint64ByUint64(uint64(upgradeVersionOffset), 0)
	_ = sto.SetUint64ByUint64(uint64(upgradeTimestampOffset), 0)
	if desiredMETAosVersion >= 2 {
		_ = sto.SetByUint64(uint64(networkFeeAccountOffset), util.AddressToHash(initialChainOwner))
	} else {
		_ = sto.SetByUint64(uint64(networkFeeAccountOffset), common.Hash{}) // the 0 address until an owner sets it
	}
	_ = sto.SetByUint64(uint64(chainIdOffset), common.BigToHash(chainConfig.ChainID))
	chainConfigStorage := sto.OpenStorageBackedBytes(chainConfigSubspace)
	_ = chainConfigStorage.Set(initMessage.SerializedChainConfig)
	_ = sto.SetUint64ByUint64(uint64(genesisBlockNumOffset), chainConfig.metachainChainParams.GenesisBlockNum)
	_ = sto.SetUint64ByUint64(uint64(brotliCompressionLevelOffset), 0) // default brotliCompressionLevel for fast compression is 0

	initialRewardsRecipient := l1pricing.BatchPosterAddress
	if desiredMETAosVersion >= 2 {
		initialRewardsRecipient = initialChainOwner
	}
	_ = l1pricing.InitializeL1PricingState(sto.OpenCachedSubStorage(l1PricingSubspace), initialRewardsRecipient, initMessage.InitialL1BaseFee)
	_ = l2pricing.InitializeL2PricingState(sto.OpenCachedSubStorage(l2PricingSubspace))
	_ = retryables.InitializeRetryableState(sto.OpenCachedSubStorage(retryablesSubspace))
	addressTable.Initialize(sto.OpenCachedSubStorage(addressTableSubspace))
	merkleAccumulator.InitializeMerkleAccumulator(sto.OpenCachedSubStorage(sendMerkleSubspace))
	blockhash.InitializeBlockhashes(sto.OpenCachedSubStorage(blockhashesSubspace))

	ownersStorage := sto.OpenCachedSubStorage(chainOwnerSubspace)
	_ = addressSet.Initialize(ownersStorage)
	_ = addressSet.OpenAddressSet(ownersStorage).Add(initialChainOwner)

	aState, err := OpenMETAosState(stateDB, burner)
	if err != nil {
		return nil, err
	}
	if desiredMETAosVersion > 1 {
		err = aState.UpgradeMETAosVersion(desiredMETAosVersion, true, stateDB, chainConfig)
		if err != nil {
			return nil, err
		}
	}
	return aState, nil
}

func (state *METAosState) UpgradeMETAosVersionIfNecessary(
	currentTimestamp uint64, stateDB vm.StateDB, chainConfig *params.ChainConfig,
) error {
	upgradeTo, err := state.upgradeVersion.Get()
	state.Restrict(err)
	flagday, _ := state.upgradeTimestamp.Get()
	if state.METAosVersion < upgradeTo && currentTimestamp >= flagday {
		return state.UpgradeMETAosVersion(upgradeTo, false, stateDB, chainConfig)
	}
	return nil
}

var ErrFatalNodeOutOfDate = errors.New("please upgrade to the latest version of the node software")

func (state *METAosState) UpgradeMETAosVersion(
	upgradeTo uint64, firstTime bool, stateDB vm.StateDB, chainConfig *params.ChainConfig,
) error {
	for state.METAosVersion < upgradeTo {
		ensure := func(err error) {
			if err != nil {
				message := fmt.Sprintf(
					"Failed to upgrade METAOS version %v to version %v: %v",
					state.METAosVersion, state.METAosVersion+1, err,
				)
				panic(message)
			}
		}

		switch state.METAosVersion {
		case 1:
			ensure(state.l1PricingState.SetLastSurplus(common.Big0, 1))
		case 2:
			ensure(state.l1PricingState.SetPerBatchGasCost(0))
			ensure(state.l1PricingState.SetAmortizedCostCapBips(math.MaxUint64))
		case 3:
			// no state changes needed
		case 4:
			// no state changes needed
		case 5:
			// no state changes needed
		case 6:
			// no state changes needed
		case 7:
			// no state changes needed
		case 8:
			// no state changes needed
		case 9:
			ensure(state.l1PricingState.SetL1FeesAvailable(stateDB.GetBalance(
				l1pricing.L1PricerFundsPoolAddress,
			)))
		case 10:
			// Update the PerBatchGasCost to a more accurate value compared to the old v6 default.
			ensure(state.l1PricingState.SetPerBatchGasCost(l1pricing.InitialPerBatchGasCostV12))

			// We had mistakenly initialized AmortizedCostCapBips to math.MaxUint64 in older versions,
			// but the correct value to disable the amortization cap is 0.
			oldAmortizationCap, err := state.l1PricingState.AmortizedCostCapBips()
			ensure(err)
			if oldAmortizationCap == math.MaxUint64 {
				ensure(state.l1PricingState.SetAmortizedCostCapBips(0))
			}

			// Clear chainOwners list to allow rectification of the mapping.
			if !firstTime {
				ensure(state.chainOwners.ClearList())
			}
		// METAOS versions 12 through 19 are left to Orbit chains for custom upgrades.
		// TODO: currently you can't get to METAOS 20 without hitting the default case.
		case 19:
			if !chainConfig.DebugMode() {
				// This upgrade isn't finalized so we only want to support it for testing
				return fmt.Errorf(
					"the chain is upgrading to unsupported METAOS version %v, %w",
					state.METAosVersion+1,
					ErrFatalNodeOutOfDate,
				)
			}
			// Update Brotli compression level for fast compression from 0 to 1
			ensure(state.SetBrotliCompressionLevel(1))
		default:
			return fmt.Errorf(
				"the chain is upgrading to unsupported METAOS version %v, %w",
				state.METAosVersion+1,
				ErrFatalNodeOutOfDate,
			)
		}
		state.METAosVersion++
	}

	if firstTime && upgradeTo >= 6 {
		if upgradeTo < 11 {
			state.Restrict(state.l1PricingState.SetPerBatchGasCost(l1pricing.InitialPerBatchGasCostV6))
		}
		state.Restrict(state.l1PricingState.SetEquilibrationUnits(l1pricing.InitialEquilibrationUnitsV6))
		state.Restrict(state.l2PricingState.SetSpeedLimitPerSecond(l2pricing.InitialSpeedLimitPerSecondV6))
		state.Restrict(state.l2PricingState.SetMaxPerBlockGasLimit(l2pricing.InitialPerBlockGasLimitV6))
	}

	state.Restrict(state.backingStorage.SetUint64ByUint64(uint64(versionOffset), state.METAosVersion))

	return nil
}

func (state *METAosState) ScheduleMETAOSUpgrade(newVersion uint64, timestamp uint64) error {
	err := state.upgradeVersion.Set(newVersion)
	if err != nil {
		return err
	}
	return state.upgradeTimestamp.Set(timestamp)
}

func (state *METAosState) GetScheduledUpgrade() (uint64, uint64, error) {
	version, err := state.upgradeVersion.Get()
	if err != nil {
		return 0, 0, err
	}
	timestamp, err := state.upgradeTimestamp.Get()
	if err != nil {
		return 0, 0, err
	}
	return version, timestamp, nil
}

func (state *METAosState) BackingStorage() *storage.Storage {
	return state.backingStorage
}

func (state *METAosState) Restrict(err error) {
	state.Burner.Restrict(err)
}

func (state *METAosState) METAOSVersion() uint64 {
	return state.METAosVersion
}

func (state *METAosState) SetFormatVersion(val uint64) {
	state.METAosVersion = val
	state.Restrict(state.backingStorage.SetUint64ByUint64(uint64(versionOffset), val))
}

func (state *METAosState) BrotliCompressionLevel() (uint64, error) {
	return state.brotliCompressionLevel.Get()
}

func (state *METAosState) SetBrotliCompressionLevel(val uint64) error {
	if val <= METAcompress.LEVEL_WELL {
		return state.brotliCompressionLevel.Set(val)
	}
	return errors.New("invalid brotli compression level")
}

func (state *METAosState) RetryableState() *retryables.RetryableState {
	return state.retryableState
}

func (state *METAosState) L1PricingState() *l1pricing.L1PricingState {
	return state.l1PricingState
}

func (state *METAosState) L2PricingState() *l2pricing.L2PricingState {
	return state.l2PricingState
}

func (state *METAosState) AddressTable() *addressTable.AddressTable {
	return state.addressTable
}

func (state *METAosState) ChainOwners() *addressSet.AddressSet {
	return state.chainOwners
}

func (state *METAosState) SendMerkleAccumulator() *merkleAccumulator.MerkleAccumulator {
	if state.sendMerkle == nil {
		state.sendMerkle = merkleAccumulator.OpenMerkleAccumulator(state.backingStorage.OpenCachedSubStorage(sendMerkleSubspace))
	}
	return state.sendMerkle
}

func (state *METAosState) Blockhashes() *blockhash.Blockhashes {
	return state.blockhashes
}

func (state *METAosState) NetworkFeeAccount() (common.Address, error) {
	return state.networkFeeAccount.Get()
}

func (state *METAosState) SetNetworkFeeAccount(account common.Address) error {
	return state.networkFeeAccount.Set(account)
}

func (state *METAosState) InfraFeeAccount() (common.Address, error) {
	return state.infraFeeAccount.Get()
}

func (state *METAosState) SetInfraFeeAccount(account common.Address) error {
	return state.infraFeeAccount.Set(account)
}

func (state *METAosState) Keccak(data ...[]byte) ([]byte, error) {
	return state.backingStorage.Keccak(data...)
}

func (state *METAosState) KeccakHash(data ...[]byte) (common.Hash, error) {
	return state.backingStorage.KeccakHash(data...)
}

func (state *METAosState) ChainId() (*big.Int, error) {
	return state.chainId.Get()
}

func (state *METAosState) ChainConfig() ([]byte, error) {
	return state.chainConfig.Get()
}

func (state *METAosState) SetChainConfig(serializedChainConfig []byte) error {
	return state.chainConfig.Set(serializedChainConfig)
}

func (state *METAosState) GenesisBlockNum() (uint64, error) {
	return state.genesisBlockNum.Get()
}
