package core

import (
	"diablo-benchmark/blockchains"
	"diablo-benchmark/blockchains/clientinterfaces"
	"diablo-benchmark/blockchains/workloadgenerators"
	"diablo-benchmark/communication"
	"diablo-benchmark/core/configs"
	"diablo-benchmark/core/handlers"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"go.uber.org/zap"
)

type Secondary struct {
	ID              int                                  // This secondary's unique ID
	ChainConfig     *configs.ChainConfig                 // Chain configuration
	Blockchain      clientinterfaces.BlockchainInterface // Blockchain Interface
	PrimaryComms    *communication.ConnClient            // Connection to the primary
	WorkloadHandler *handlers.WorkloadHandler            // Workload Handler
}

// Create a new secondary, set up the things we need.
func NewSecondary(config *configs.ChainConfig, primaryAddress string) (*Secondary, error) {
	// Set up the communication
	c, err := communication.SetupSecondaryTCP(primaryAddress)
	if err != nil {
		zap.L().Error("failed to connect to primary server")
		return nil, err
	}

	// Log and return, ready to go!
	zap.L().Info("Secondary init")
	return &Secondary{
		ChainConfig:  config,
		PrimaryComms: c,
	}, nil
}

// Runs the main secondary things, sets up the secondary and waits for the commands
func (s *Secondary) Run() {
	// Main work loop that handles the commands from primary and dispatches
	// the workload from the benchmark.
	for {

		cmd, err := s.PrimaryComms.InitialRead()

		if err != nil {
			zap.L().Warn("failed to read",
				zap.String("err", err.Error()))

			s.PrimaryComms.CloseConn()
			return
		}

		switch cmd[0] {
		case communication.MsgPrepare[0]:
			// Prepare message, did we connect, and are we prepared for work?
			zap.L().Info("Got command from primary",
				zap.String("CMD", "PREPARE"))
			// It should also give us a secondary ID as aux value
			s.ID = int(cmd[1])
			s.ID = int(binary.BigEndian.Uint32(cmd[1:5]))
			numThreads := binary.BigEndian.Uint32(cmd[5:9])
			// Connect le blockchains
			var bcis []clientinterfaces.BlockchainInterface
			for i := uint32(0); i < numThreads; i++ {
				bc, err := clientinterfaces.GetBlockchainInterface(s.ChainConfig)
				if err != nil {
					s.PrimaryComms.ReplyERR(err.Error())
				}
				bcis = append(bcis, bc)
			}

			// Create the workload handler
			wHandler := handlers.NewWorkloadHandler(
				numThreads,
				bcis,
			)

			s.WorkloadHandler = wHandler

			err := s.WorkloadHandler.Connect(s.ChainConfig.Nodes, s.ID)
			if err != nil {
				s.PrimaryComms.ReplyERR(err.Error())
				continue
			}
		case communication.MsgBc[0]:
			// What type of blockchain are we running?
			// NOTE: see blockchains/bctypemessage.go for details about why feature
			// is not used (for now).
			zap.L().Info("Got command from primary",
				zap.String("CMD", "BLOCKCHAIN"))
			_, err = blockchains.MatchMessageToInterface(cmd[1])
			if err != nil {
				s.PrimaryComms.ReplyERR(err.Error())
				continue
			}
		case communication.MsgWorkload[0]:
			zap.L().Info("Got command from primary",
				zap.String("CMD", "WORKLOAD"))

			// Workload length = bytes 1-4
			workloadLen := binary.BigEndian.Uint64(cmd[1:])
			wl, err := s.PrimaryComms.ReadSize(workloadLen)

			if err != nil {
				zap.L().Warn("failed to read workload bytes",
					zap.String("err", err.Error()))
				s.PrimaryComms.ReplyERR(err.Error())
				continue
			}

			var unmarshaledWorkload workloadgenerators.SecondaryWorkload
			err = json.Unmarshal(wl, &unmarshaledWorkload)

			if err != nil {
				zap.L().Warn("failed to unmarshal workload",
					zap.String("err", err.Error()))
				s.PrimaryComms.ReplyERR(err.Error())
				continue
			}

			err = s.WorkloadHandler.ParseWorkloads(unmarshaledWorkload)
			if err != nil {
				zap.L().Warn("failed to parse workload",
					zap.String("err", err.Error()))
				s.PrimaryComms.ReplyERR(err.Error())
				continue
			}

		case communication.MsgRun[0]:
			zap.L().Info("Got command from primary",
				zap.String("CMD", "RUN"))
			errs := s.WorkloadHandler.RunBench()
			if errs != nil {
				zap.L().Warn("error during bench",
					zap.Error(err))
				s.PrimaryComms.ReplyERR(err.Error())
				continue
			}
		case communication.MsgResults[0]:
			zap.L().Info("Got command from primary",
				zap.String("CMD", "RESULTS"))
			res := s.WorkloadHandler.HandleCleanup()
			resBytes, err := json.Marshal(res)
			if err != nil {
				s.PrimaryComms.ReplyERR("failed to convert results to bytes")
			}
			fmt.Println(resBytes)
			s.PrimaryComms.SendDataOK(resBytes)
		case communication.MsgFin[0]:
			zap.L().Info("Got command from primary",
				zap.String("CMD", "FIN"))
			s.WorkloadHandler.CloseAll()
			return
		default:
			s.PrimaryComms.ReplyERR("no matching command")
			continue
		}

		s.PrimaryComms.ReplyOK()
	}

}