package db

import (
	"bytes"
	"database/sql"
	"eth2-exporter/types"
	"fmt"
	"github.com/jmoiron/sqlx"
	"github.com/lib/pq"
	ethpb "github.com/prysmaticlabs/prysm/proto/eth/v1alpha1"
	"github.com/sirupsen/logrus"
	"math/big"
)

var DB *sqlx.DB
var logger = logrus.New().WithField("module", "db")

func GetLatestEpoch() (uint64, error) {
	var epoch uint64

	err := DB.Get(&epoch, "SELECT COALESCE(MAX(epoch), 0) FROM epochs")

	if err != nil {
		return 0, fmt.Errorf("error retrieving latest epoch from DB: %v", err)
	}

	return epoch, nil
}

func GetAllEpochs() ([]uint64, error) {
	var epochs []uint64
	err := DB.Select(&epochs, "SELECT epoch FROM epochs ORDER BY epoch")

	if err != nil {
		return nil, fmt.Errorf("error retrieving all epochs from DB: %v", err)
	}

	return epochs, nil
}

func GetLastBlocks(startEpoch, endEpoch uint64) ([]*types.MinimalBlock, error) {
	var blocks []*types.MinimalBlock

	err := DB.Select(&blocks, "SELECT epoch, slot, blockroot FROM blocks WHERE epoch >= $1 AND epoch <= $2 ORDER BY slot DESC", startEpoch, endEpoch)

	if err != nil {
		return nil, fmt.Errorf("error retrieving last blocks from DB: %v", err)
	}

	return blocks, nil
}

func SaveAttestationPool(attestations []*ethpb.Attestation) error {
	tx, err := DB.Begin()
	if err != nil {
		return fmt.Errorf("error starting db transactions: %v", err)
	}
	defer tx.Rollback()

	_, err = tx.Exec("TRUNCATE attestationpool")
	if err != nil {
		return fmt.Errorf("error truncating attestationpool table: %v", err)
	}

	stmtAttestationPool, err := tx.Prepare(`INSERT INTO attestationpool (aggregationbits, custodybits, signature, slot, index, beaconblockroot, source_epoch, source_root, target_epoch, target_root)
 													VALUES    ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10) ON CONFLICT (slot, index) DO NOTHING`)
	if err != nil {
		return err
	}
	defer stmtAttestationPool.Close()

	for _, attestation := range attestations {
		_, err := stmtAttestationPool.Exec(attestation.AggregationBits.Bytes(), attestation.CustodyBits.Bytes(), attestation.Signature, attestation.Data.Slot, attestation.Data.Index, attestation.Data.BeaconBlockRoot, attestation.Data.Source.Epoch, attestation.Data.Source.Root, attestation.Data.Target.Epoch, attestation.Data.Target.Root)
		if err != nil {
			return fmt.Errorf("error executing stmtAttestationPool: %v", err)
		}
	}

	err = tx.Commit()
	if err != nil {
		return fmt.Errorf("error committing db transaction: %v", err)
	}
	return nil
}

func SaveValidatorQueue(validators *ethpb.ValidatorQueue) error {
	tx, err := DB.Begin()
	if err != nil {
		return fmt.Errorf("error starting db transactions: %v", err)
	}
	defer tx.Rollback()

	_, err = tx.Exec("TRUNCATE validatorqueue_activation")
	if err != nil {
		return fmt.Errorf("error truncating validatorqueue_activation table: %v", err)
	}
	_, err = tx.Exec("TRUNCATE validatorqueue_exit")
	if err != nil {
		return fmt.Errorf("error truncating validatorqueue_exit table: %v", err)
	}

	stmtValidatorQueueActivation, err := tx.Prepare(`INSERT INTO validatorqueue_activation (index, publickey)
 													VALUES    ($1, $2)`)
	if err != nil {
		return err
	}
	defer stmtValidatorQueueActivation.Close()

	stmtValidatorQueueExit, err := tx.Prepare(`INSERT INTO validatorqueue_exit (index, publickey)
 													VALUES    ($1, $2)`)
	if err != nil {
		return err
	}
	defer stmtValidatorQueueExit.Close()

	for i, publickey := range validators.ActivationPublicKeys {
		_, err := stmtValidatorQueueActivation.Exec(i, publickey)
		if err != nil {
			return fmt.Errorf("error executing stmtValidatorQueueActivation: %v", err)
		}
	}
	for i, publickey := range validators.ExitPublicKeys {
		_, err := stmtValidatorQueueExit.Exec(i, publickey)
		if err != nil {
			return fmt.Errorf("error executing stmtValidatorQueueExit: %v", err)
		}
	}

	err = tx.Commit()
	if err != nil {
		return fmt.Errorf("error committing db transaction: %v", err)
	}
	return nil
}

func SaveEpoch(data *types.EpochData) error {
	tx, err := DB.Begin()
	if err != nil {
		return fmt.Errorf("error starting db transactions: %v", err)
	}
	defer tx.Rollback()

	err = saveValidatorSet(data.Epoch, data.Validators, tx)
	if err != nil {
		return fmt.Errorf("error saving validator set to db: %v", err)
	}

	err = saveValidatorAssignments(data.Epoch, data.ValidatorAssignmentes, tx)
	if err != nil {
		return fmt.Errorf("error saving validator assignments to db: %v", err)
	}

	err = saveBeaconCommittees(data.Epoch, data.BeaconCommittees, tx)
	if err != nil {
		return fmt.Errorf("error saving beacon committees to db: %v", err)
	}

	err = saveValidatorBalances(data.Epoch, data.ValidatorBalances, tx)
	if err != nil {
		return fmt.Errorf("error saving validator balances to db: %v", err)
	}

	err = saveBlocks(data.Epoch, data.Blocks, tx)
	if err != nil {
		return fmt.Errorf("error saving blocks to db: %v", err)
	}

	proposerSlashingsCount := 0
	attesterSlashingsCount := 0
	attestationsCount := 0
	depositCount := 0
	voluntaryExitCount := 0

	for _, b := range data.Blocks {
		proposerSlashingsCount += len(b.Block.Block.Body.ProposerSlashings)
		attesterSlashingsCount += len(b.Block.Block.Body.AttesterSlashings)
		attestationsCount += len(b.Block.Block.Body.Attestations)
		depositCount += len(b.Block.Block.Body.Deposits)
		voluntaryExitCount += len(b.Block.Block.Body.VoluntaryExits)
	}

	validatorBalanceSum := new(big.Int)
	for _, b := range data.ValidatorBalances {
		validatorBalanceSum = new(big.Int).Add(validatorBalanceSum, new(big.Int).SetUint64(b.Balance))
	}

	validatorBalanceAverage := new(big.Int).Div(validatorBalanceSum, new(big.Int).SetInt64(int64(len(data.Validators)))).Uint64()

	validatorsCount := 0
	for _, v := range data.Validators {
		if v.ExitEpoch > data.Epoch {
			validatorsCount++
		}
	}

	_, err = tx.Exec(`INSERT INTO epochs (
												epoch, 
												blockscount, 
												proposerslashingscount, 
												attesterslashingscount, 
												attestationscount, 
												depositscount, 
												voluntaryexitscount, 
												validatorscount, 
												averagevalidatorbalance, 
												finalized, eligibleether, 
												globalparticipationrate, 
												votedether
												)
							VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13) ON CONFLICT (epoch) DO UPDATE SET 
								  blockscount = excluded.blockscount, 
								  proposerslashingscount = excluded.proposerslashingscount,
								  attesterslashingscount = excluded.attesterslashingscount,
								  attestationscount = excluded.attestationscount,
								  depositscount = excluded.depositscount,
								  voluntaryexitscount = excluded.voluntaryexitscount,
								  validatorscount = excluded.validatorscount,
								  averagevalidatorbalance = excluded.averagevalidatorbalance,
								  finalized = excluded.finalized,
								  eligibleether = excluded.eligibleether,
								  globalparticipationrate = excluded.globalparticipationrate,
								  votedether = excluded.votedether`,
		data.Epoch,
		len(data.Blocks),
		proposerSlashingsCount,
		attesterSlashingsCount,
		attestationsCount,
		depositCount,
		voluntaryExitCount,
		validatorsCount,
		validatorBalanceAverage,
		data.EpochParticipationStats.Finalized,
		data.EpochParticipationStats.Participation.EligibleEther,
		data.EpochParticipationStats.Participation.GlobalParticipationRate,
		data.EpochParticipationStats.Participation.VotedEther)

	if err != nil {
		return fmt.Errorf("error executing save epoch statement: %v", err)
	}

	err = tx.Commit()
	if err != nil {
		return fmt.Errorf("error committing db transaction: %v", err)
	}
	return nil
}

func saveValidatorSet(epoch uint64, validators map[string]*ethpb.Validator, tx *sql.Tx) error {

	stmt, err := tx.Prepare(`INSERT INTO validator_set (epoch, pubkey, withdrawableepoch, withdrawalcredentials, effectivebalance, slashed, activationeligibilityepoch, activationepoch, exitepoch)
 													VALUES    ($1, $2, $3, $4, $5, $6, $7, $8, $9) ON CONFLICT (epoch, pubkey) DO NOTHING`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	for _, v := range validators {
		if v.WithdrawableEpoch == 18446744073709551615 {
			v.WithdrawableEpoch = 9223372036854775807
		}
		if v.ExitEpoch == 18446744073709551615 {
			v.ExitEpoch = 9223372036854775807
		}
		if v.ActivationEligibilityEpoch == 18446744073709551615 {
			v.ActivationEligibilityEpoch = 9223372036854775807
		}
		if v.ActivationEpoch == 18446744073709551615 {
			v.ActivationEpoch = 9223372036854775807
		}
		_, err := stmt.Exec(epoch, v.PublicKey, v.WithdrawableEpoch, v.WithdrawalCredentials, v.EffectiveBalance, v.Slashed, v.ActivationEligibilityEpoch, v.ActivationEpoch, v.ExitEpoch)
		if err != nil {
			return fmt.Errorf("error executing save validator set statement: %v", err)
		}
		//logger.Printf("Exported validator %x for epoch %v", v.PublicKey, epoch)
	}

	return nil
}

func saveValidatorAssignments(epoch uint64, assignments []*ethpb.ValidatorAssignments_CommitteeAssignment, tx *sql.Tx) error {

	stmt, err := tx.Prepare(`INSERT INTO validator_assignments (epoch, pubkey, beaconcommittees, committeeindex, attesterslot, proposerslot)
 													VALUES    ($1, $2, $3, $4, $5, $6) ON CONFLICT (epoch, pubkey) DO NOTHING`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	for _, a := range assignments {
		_, err := stmt.Exec(epoch, a.PublicKey, pq.Array(a.BeaconCommittees), a.CommitteeIndex, a.AttesterSlot, a.ProposerSlot)
		if err != nil {
			return fmt.Errorf("error executing save validator assignment statement: %v", err)
		}
	}

	return nil
}

func saveBeaconCommittees(epoch uint64, committees []*ethpb.BeaconCommittees_CommitteeItem, tx *sql.Tx) error {

	stmt, err := tx.Prepare(`INSERT INTO beacon_committees (epoch, slot, slotindex, indices)
 													VALUES    ($1, $2, $3, $4) ON CONFLICT (epoch, slot, slotindex) DO NOTHING`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	for i, c := range committees {
		_, err := stmt.Exec(epoch, c.Slot, i, pq.Array(c.Committee))
		if err != nil {
			return fmt.Errorf("error executing save beacon committee statement: %v", err)
		}

		//logger.Printf("Exported validator %x assignments for epoch %v", a.PublicKey, epoch)
	}

	return nil
}

func saveValidatorBalances(epoch uint64, balances map[string]*ethpb.ValidatorBalances_Balance, tx *sql.Tx) error {

	stmt, err := tx.Prepare(`INSERT INTO validator_balances (epoch, pubkey, balance, index)
 													VALUES    ($1, $2, $3, $4) ON CONFLICT (epoch, pubkey) DO UPDATE SET balance = excluded.balance`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	for _, b := range balances {
		_, err := stmt.Exec(epoch, b.PublicKey, b.Balance, b.Index)
		if err != nil {
			return fmt.Errorf("error executing save validator balance statement: %v", err)
		}

		//logger.Printf("Exported validator %x assignments for epoch %v", a.PublicKey, epoch)
	}

	return nil
}

func saveBlocks(epoch uint64, blocks map[uint64]*types.BlockContainer, tx *sql.Tx) error {

	stmtBlock, err := tx.Prepare(`INSERT INTO blocks (
                    epoch, 
                    slot, 
                    blockroot, 
                    parentroot, 
                    stateroot, 
                    signature, 
                    randaoreveal,
                    graffiti, 
                    eth1data_depositroot, 
                    eth1data_depositcount, 
                    eth1data_blockhash, 
                    proposerslashingscount, 
                    attesterslashingscount, 
                    attestationscount, 
                    depositscount, 
                    voluntaryexitscount, 
                    proposer,
                    status)
 					VALUES    ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18) ON CONFLICT (slot) DO NOTHING`)
	if err != nil {
		return err
	}
	defer stmtBlock.Close()

	stmtProposerSlashing, err := tx.Prepare(`INSERT INTO blocks_proposerslashings (block_slot, block_index, proposerindex, header1_slot, header1_parentroot, header1_stateroot, header1_bodyroot, header1_signature, header2_slot, header2_parentroot, header2_stateroot, header2_bodyroot, header2_signature)
 													VALUES    ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13) ON CONFLICT (block_slot, block_index) DO NOTHING`)
	if err != nil {
		return err
	}
	defer stmtProposerSlashing.Close()

	stmtAttesterSlashing, err := tx.Prepare(`INSERT INTO blocks_attesterslashings (block_slot, block_index, attestation1_custodybit_0indices, attestation1_custodybit_1indices, attestation1_signature, attestation1_slot, attestation1_index, attestation1_beaconblockroot, attestation1_source_epoch, attestation1_source_root, attestation1_target_epoch, attestation1_target_root, attestation2_custodybit_0indices, attestation2_custodybit_1indices, attestation2_signature, attestation2_slot, attestation2_index, attestation2_beaconblockroot, attestation2_source_epoch, attestation2_source_root, attestation2_target_epoch, attestation2_target_root)
 													VALUES    ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21, $22) ON CONFLICT (block_slot, block_index) DO NOTHING`)
	if err != nil {
		return err
	}
	defer stmtAttesterSlashing.Close()

	stmtAttestations, err := tx.Prepare(`INSERT INTO blocks_attestations (block_slot, block_index, aggregationbits, custodybits, signature, slot, index, beaconblockroot, source_epoch, source_root, target_epoch, target_root)
 													VALUES    ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12) ON CONFLICT (block_slot, block_index) DO NOTHING`)
	if err != nil {
		return err
	}
	defer stmtAttestations.Close()

	stmtDeposits, err := tx.Prepare(`INSERT INTO blocks_deposits (block_slot, block_index, proof, publickey, withdrawalcredentials, amount, signature)
 													VALUES    ($1, $2, $3, $4, $5, $6, $7) ON CONFLICT (block_slot, block_index) DO NOTHING`)
	if err != nil {
		return err
	}
	defer stmtDeposits.Close()

	stmtVoluntaryExits, err := tx.Prepare(`INSERT INTO blocks_voluntaryexits (block_slot, block_index, epoch, validatorindex, signature)
 													VALUES    ($1, $2, $3, $4, $5) ON CONFLICT (block_slot, block_index) DO NOTHING`)
	if err != nil {
		return err
	}
	defer stmtVoluntaryExits.Close()

	for _, b := range blocks {

		var dbBlockRootHash []byte
		err := DB.Get(&dbBlockRootHash, "SELECT blockroot FROM blocks WHERE slot = $1", b.Block.Block.Slot)

		if err == nil && bytes.Compare(dbBlockRootHash, b.Block.BlockRoot) == 0 {
			logger.Printf("Skipping export of block %v as it is already present in the db", b.Block.Block.Slot)
			continue
		} else if err == nil && bytes.Compare(dbBlockRootHash, b.Block.BlockRoot) != 0 {
			logger.Printf("Deleting block %v as it has been changed due to a chain reorg", b.Block.Block.Slot)

			_, err := tx.Exec("DELETE FROM blocks WHERE slot = $1", b.Block.Block.Slot)
			if err != nil {
				return fmt.Errorf("error deleting block from blocks table: %v", err)
			}
			_, err = tx.Exec("DELETE FROM blocks_proposerslashings WHERE block_slot = $1", b.Block.Block.Slot)
			if err != nil {
				return fmt.Errorf("error deleting block from blocks_proposerslashings table: %v", err)
			}
			_, err = tx.Exec("DELETE FROM blocks_attesterslashings WHERE block_slot = $1", b.Block.Block.Slot)
			if err != nil {
				return fmt.Errorf("error deleting block from blocks_attesterslashings table: %v", err)
			}
			_, err = tx.Exec("DELETE FROM blocks_attestations WHERE block_slot = $1", b.Block.Block.Slot)
			if err != nil {
				return fmt.Errorf("error deleting block from blocks_attestations table: %v", err)
			}
			_, err = tx.Exec("DELETE FROM blocks_deposits WHERE block_slot = $1", b.Block.Block.Slot)
			if err != nil {
				return fmt.Errorf("error deleting block from blocks_deposits table: %v", err)
			}
			_, err = tx.Exec("DELETE FROM blocks_voluntaryexits WHERE block_slot = $1", b.Block.Block.Slot)
			if err != nil {
				return fmt.Errorf("error deleting block from blocks_voluntaryexits table: %v", err)
			}
		}

		_, err = stmtBlock.Exec(epoch,
			b.Block.Block.Slot,
			b.Block.BlockRoot,
			b.Block.Block.ParentRoot,
			b.Block.Block.StateRoot,
			b.Block.Block.Signature,
			b.Block.Block.Body.RandaoReveal,
			b.Block.Block.Body.Graffiti,
			b.Block.Block.Body.Eth1Data.DepositRoot,
			b.Block.Block.Body.Eth1Data.DepositCount,
			b.Block.Block.Body.Eth1Data.BlockHash,
			len(b.Block.Block.Body.ProposerSlashings),
			len(b.Block.Block.Body.AttesterSlashings),
			len(b.Block.Block.Body.Attestations),
			len(b.Block.Block.Body.Deposits),
			len(b.Block.Block.Body.VoluntaryExits),
			b.Proposer,
			b.Status)
		if err != nil {
			return fmt.Errorf("error executing stmtBlocks: %v", err)
		}

		for i, ps := range b.Block.Block.Body.ProposerSlashings {
			_, err := stmtProposerSlashing.Exec(b.Block.Block.Slot, i, ps.ProposerIndex, ps.Header_1.Slot, ps.Header_1.ParentRoot, ps.Header_1.StateRoot, ps.Header_1.BodyRoot, ps.Header_1.Signature, ps.Header_2.Slot, ps.Header_2.ParentRoot, ps.Header_2.StateRoot, ps.Header_2.BodyRoot, ps.Header_2.Signature)
			if err != nil {
				return fmt.Errorf("error executing stmtProposerSlashing: %v", err)
			}
		}

		for i, as := range b.Block.Block.Body.AttesterSlashings {
			_, err := stmtAttesterSlashing.Exec(b.Block.Block.Slot, i, pq.Array(as.Attestation_1.CustodyBit_0Indices), pq.Array(as.Attestation_1.CustodyBit_1Indices), as.Attestation_1.Signature, as.Attestation_1.Data.Slot, as.Attestation_1.Data.Index, as.Attestation_1.Data.BeaconBlockRoot, as.Attestation_1.Data.Source.Epoch, as.Attestation_1.Data.Source.Root, as.Attestation_1.Data.Target.Epoch, as.Attestation_1.Data.Target.Root, pq.Array(as.Attestation_2.CustodyBit_0Indices), pq.Array(as.Attestation_2.CustodyBit_1Indices), as.Attestation_2.Signature, as.Attestation_2.Data.Slot, as.Attestation_2.Data.Index, as.Attestation_2.Data.BeaconBlockRoot, as.Attestation_2.Data.Source.Epoch, as.Attestation_2.Data.Source.Root, as.Attestation_2.Data.Target.Epoch, as.Attestation_2.Data.Target.Root)
			if err != nil {
				return fmt.Errorf("error executing stmtAttesterSlashing: %v", err)
			}
		}

		for i, a := range b.Block.Block.Body.Attestations {
			_, err := stmtAttestations.Exec(b.Block.Block.Slot, i, a.AggregationBits.Bytes(), a.CustodyBits.Bytes(), a.Signature, a.Data.Slot, a.Data.Index, a.Data.BeaconBlockRoot, a.Data.Source.Epoch, a.Data.Source.Root, a.Data.Target.Epoch, a.Data.Target.Root)
			if err != nil {
				return fmt.Errorf("error executing stmtAttestations: %v", err)
			}
		}

		for i, d := range b.Block.Block.Body.Deposits {
			_, err := stmtDeposits.Exec(b.Block.Block.Slot, i, nil, d.Data.PublicKey, d.Data.WithdrawalCredentials, d.Data.Amount, d.Data.Signature)
			if err != nil {
				return fmt.Errorf("error executing stmtDeposits: %v", err)
			}
		}

		for i, ve := range b.Block.Block.Body.VoluntaryExits {
			_, err := stmtVoluntaryExits.Exec(b.Block.Block.Slot, i, ve.Epoch, ve.ValidatorIndex, ve.Signature)
			if err != nil {
				return fmt.Errorf("error executing stmtVoluntaryExits: %v", err)
			}
		}
	}

	return nil
}
