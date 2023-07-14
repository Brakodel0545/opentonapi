package bath

import (
	"fmt"
	"strings"

	"github.com/tonkeeper/opentonapi/pkg/core"
	"github.com/tonkeeper/tongo"
	"github.com/tonkeeper/tongo/abi"
	"github.com/tonkeeper/tongo/tlb"
)

// Bubble represents a transaction in the beginning.
// But we can merge neighbour bubbles together
// if we find a known action pattern like an NFT Transfer or a SmartContractExecution in a trace.
type Bubble struct {
	Info      actioner
	Accounts  []tongo.AccountID
	Children  []*Bubble
	ValueFlow *ValueFlow
	// ContractDeployments specifies a list of contracts initialized by this bubble.
	ContractDeployments map[tongo.AccountID]ContractDeployment
}

// ContractDeployment holds information about initialization of a contract.
// TODO: should ContractDeployment contains LT/time of a deployment so we can sort several ContractDeploy actions?
type ContractDeployment struct {
	//// initInterfaces is a list of interfaces implemented by the code of stateInit.
	initInterfaces []abi.ContractInterface
	success        bool
}

func (b Bubble) String() string {
	var buf strings.Builder
	fmt.Fprintf(&buf, "%T: ", b.Info)
	prefix := "    "
	fmt.Fprintf(&buf, " %+v\n", b.Info)
	for _, c := range b.Children {
		for _, l := range strings.Split(c.String(), "\n") {
			if l == "" {
				continue
			}
			buf.WriteString(prefix + l + "\n")
		}
	}
	return buf.String()
}

// MergeContractDeployments copies contract deployments from other bubble.
func (b *Bubble) MergeContractDeployments(other *Bubble) {
	if b.ContractDeployments == nil {
		b.ContractDeployments = make(map[tongo.AccountID]ContractDeployment, len(other.ContractDeployments))
	}
	for accountID, deployment := range other.ContractDeployments {
		b.ContractDeployments[accountID] = deployment
	}
}

type actioner interface {
	ToAction() *Action
}

func FromTrace(trace *core.Trace) *Bubble {
	return fromTrace(trace)
}

func fromTrace(trace *core.Trace) *Bubble {
	btx := BubbleTx{
		success:                         trace.Success,
		transactionType:                 trace.Transaction.Type,
		account:                         Account{Address: trace.Account, Interfaces: trace.AccountInterfaces},
		external:                        trace.InMsg == nil || trace.InMsg.IsExternal(),
		accountWasActiveAtComputingTime: trace.Type != core.OrdinaryTx || trace.ComputePhase == nil || trace.ComputePhase.SkipReason != tlb.ComputeSkipReasonNoState,
		additionalInfo:                  trace.AdditionalInfo,
	}

	accounts := []tongo.AccountID{trace.Account}
	var source *Account
	if trace.InMsg != nil && trace.InMsg.Source != nil {
		source = &Account{
			Address: *trace.InMsg.Source,
		}
		accounts = append(accounts, source.Address)
	}
	var inputAmount int64
	var initInterfaces []abi.ContractInterface
	if msg := trace.InMsg; msg != nil {
		btx.bounce = msg.Bounce
		btx.bounced = msg.Bounced
		btx.inputAmount = msg.Value
		inputAmount = msg.Value
		btx.opCode = msg.OpCode
		btx.decodedBody = msg.DecodedBody
		btx.inputFrom = source
		btx.init = msg.Init
		initInterfaces = msg.InitInterfaces
	}
	aggregatedFee := trace.TotalFee
	b := Bubble{
		Info:                btx,
		Accounts:            accounts,
		Children:            make([]*Bubble, len(trace.Children)),
		ContractDeployments: map[tongo.AccountID]ContractDeployment{},
		ValueFlow: &ValueFlow{
			Accounts: map[tongo.AccountID]*AccountValueFlow{
				trace.Account: {
					Ton: inputAmount,
				},
			},
		},
	}
	contractDeployed := trace.EndStatus == tlb.AccountActive && trace.OrigStatus != tlb.AccountActive
	if contractDeployed {
		b.ContractDeployments[trace.Account] = ContractDeployment{
			success:        btx.success,
			initInterfaces: initInterfaces,
		}
	}

	for _, outMsg := range trace.OutMsgs {
		b.ValueFlow.AddTons(trace.Account, -outMsg.Value)
		aggregatedFee += outMsg.FwdFee
	}
	for i, c := range trace.Children {
		if c.InMsg != nil {
			// If an outbound message has a corresponding InMsg,
			// we have removed it from OutMsgs to avoid duplicates.
			// That's why we add tons here
			b.ValueFlow.AddTons(trace.Account, -c.InMsg.Value)
			aggregatedFee += c.InMsg.FwdFee
		}
		b.Children[i] = fromTrace(c)
	}
	b.ValueFlow.Accounts[trace.Account].Fees = aggregatedFee
	return &b
}
