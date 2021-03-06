package handlers

import (
	"encoding/hex"
	"encoding/json"
	"eth2-exporter/db"
	"eth2-exporter/services"
	"eth2-exporter/types"
	"eth2-exporter/utils"
	"fmt"
	"github.com/gorilla/mux"
	"html/template"
	"net/http"
	"strconv"
	"strings"
	"time"
)

var validatorTemplate = template.Must(template.New("validator").ParseFiles("templates/layout.html", "templates/validator.html"))
var validatorNotFoundTemplate = template.Must(template.New("validatornotfound").ParseFiles("templates/layout.html", "templates/validatornotfound.html"))

func Validator(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html")

	vars := mux.Vars(r)
	publicKeyRaw := strings.Replace(vars["publicKey"], "0x", "", -1)

	validatorPageData := types.ValidatorPageData{}
	publicKey, err := hex.DecodeString(publicKeyRaw)

	data := &types.PageData{
		Meta: &types.Meta{
			Title:       fmt.Sprintf("Validator 0x%x - beaconcha.in - Ethereum 2.0 beacon chain explorer - %v", publicKey, time.Now().Year()),
			Description: "beaconcha.in makes the Ethereum 2.0. beacon chain accessible to non-technical end users",
			Path:        fmt.Sprintf("/validator/%v", publicKeyRaw),
		},
		Active: "validators",
		Data:   nil,
	}

	if err != nil {
		err := validatorNotFoundTemplate.ExecuteTemplate(w, "layout", data)

		if err != nil {
			logger.Fatalf("Error executing template for %v route: %v", r.URL.String(), err)
		}
		return
	}

	err = db.DB.Get(&validatorPageData, `SELECT 
											 validator_set.epoch, 
											 validator_set.pubkey, 
											 validator_set.withdrawableepoch, 
											 validator_set.effectivebalance, 
											 validator_set.slashed, 
											 validator_set.activationeligibilityepoch, 
											 validator_set.activationepoch, 
											 validator_set.exitepoch,
       										 validator_balances.balance,
       										 validator_balances.index
										FROM validator_set
										LEFT JOIN validator_balances ON validator_set.epoch = validator_balances.epoch 
										                                    AND validator_set.pubkey = validator_balances.pubkey
										WHERE validator_set.epoch = $1 
										  AND validator_set.pubkey = $2
										LIMIT 1`, services.LatestEpoch(), publicKey)

	validatorPageData.CurrentBalanceFormatted = utils.FormatBalance(validatorPageData.CurrentBalance)
	validatorPageData.EffectiveBalanceFormatted = utils.FormatBalance(validatorPageData.EffectiveBalance)

	validatorPageData.ActivationEligibilityTs = utils.EpochToTime(validatorPageData.ActivationEligibilityEpoch)
	validatorPageData.ActivationTs = utils.EpochToTime(validatorPageData.ActivationEpoch)
	validatorPageData.ExitTs = utils.EpochToTime(validatorPageData.ExitEpoch)
	validatorPageData.WithdrawableTs = utils.EpochToTime(validatorPageData.WithdrawableEpoch)

	err = db.DB.Get(&validatorPageData.ProposedBlocksCount, "SELECT COUNT(*) FROM blocks WHERE proposer = $1", publicKey)
	if err != nil {
		logger.Printf("Error retrieving proposed blocks count: %v", err)
		http.Error(w, "Internal server error", 503)
		return
	}

	var balanceHistory []*types.ValidatorBalanceHistory
	err = db.DB.Select(&balanceHistory, "SELECT epoch, balance FROM validator_balances WHERE pubkey = $1 ORDER BY epoch", publicKey)
	if err != nil {
		logger.Printf("Error retrieving validator balance history: %v", err)
		http.Error(w, "Internal server error", 503)
		return
	}

	validatorPageData.BalanceHistoryChartData = make([][]float64, len(balanceHistory))
	for i, balance := range balanceHistory {
		validatorPageData.BalanceHistoryChartData[i] = []float64{float64(utils.EpochToTime(balance.Epoch).Unix() * 1000), float64(balance.Balance) / 1000000000}
	}

	if validatorPageData.Epoch > validatorPageData.ExitEpoch {
		validatorPageData.Status = "Ejected"
	} else if validatorPageData.Epoch < validatorPageData.ActivationEpoch {
		validatorPageData.Status = "Pending"
	} else {
		validatorPageData.Status = "Active"
	}
	data.Data = validatorPageData

	err = validatorTemplate.ExecuteTemplate(w, "layout", data)

	if err != nil {
		logger.Fatalf("Error executing template for %v route: %v", r.URL.String(), err)
	}
}

func ValidatorProposedBlocks(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	vars := mux.Vars(r)
	publicKeyRaw := strings.Replace(vars["publicKey"], "0x", "", -1)
	publicKey, err := hex.DecodeString(publicKeyRaw)

	q := r.URL.Query()

	draw, err := strconv.ParseUint(q.Get("draw"), 10, 64)
	if err != nil {
		logger.Printf("Error converting datatables data parameter from string to int: %v", err)
		http.Error(w, "Internal server error", 503)
		return
	}
	start, err := strconv.ParseUint(q.Get("start"), 10, 64)
	if err != nil {
		logger.Printf("Error converting datatables start parameter from string to int: %v", err)
		http.Error(w, "Internal server error", 503)
		return
	}
	length, err := strconv.ParseUint(q.Get("length"), 10, 64)
	if err != nil {
		logger.Printf("Error converting datatables length parameter from string to int: %v", err)
		http.Error(w, "Internal server error", 503)
		return
	}

	var totalCount uint64

	err = db.DB.Get(&totalCount, "SELECT COUNT(*) FROM blocks WHERE proposer = $1", publicKey)
	if err != nil {
		logger.Printf("Error retrieving proposed blocks count: %v", err)
		http.Error(w, "Internal server error", 503)
		return
	}

	var blocks []*types.IndexPageDataBlocks
	err = db.DB.Select(&blocks, `SELECT blocks.epoch, 
											    blocks.slot,  
											    blocks.proposer,  
											    blocks.blockroot, 
											    blocks.parentroot, 
											    blocks.attestationscount, 
											    blocks.depositscount, 
											    blocks.voluntaryexitscount, 
											    blocks.proposerslashingscount, 
											    blocks.attesterslashingscount, 
											    blocks.status 
										FROM blocks 
										WHERE blocks.proposer = $1
										ORDER BY blocks.slot DESC
										LIMIT $2 OFFSET $3`, publicKey, length, start)

	if err != nil {
		logger.Printf("Error retrieving proposed blocks data: %v", err)
		http.Error(w, "Internal server error", 503)
		return
	}

	tableData := make([][]string, len(blocks))
	for i, b := range blocks {
		tableData[i] = []string{
			fmt.Sprintf("%v", b.Epoch),
			fmt.Sprintf("%v", b.Slot),
			fmt.Sprintf("%v", b.Status),
			fmt.Sprintf("%v", utils.SlotToTime(b.Slot).Unix()),
			fmt.Sprintf("%x", b.BlockRoot),
			fmt.Sprintf("%v", b.Attestations),
			fmt.Sprintf("%v", b.Deposits),
			fmt.Sprintf("%v / %v", b.Proposerslashings, b.Attesterslashings),
			fmt.Sprintf("%v", b.Exits),
		}
	}

	data := &types.DataTableResponse{
		Draw:            draw,
		RecordsTotal:    totalCount,
		RecordsFiltered: totalCount,
		Data:            tableData,
	}

	err = json.NewEncoder(w).Encode(data)
	if err != nil {
		logger.Fatalf("Error enconding json response for %v route: %v", r.URL.String(), err)
	}
}
