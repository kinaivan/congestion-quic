package congestion

import (
	"bufio"
	"fmt"
	"log"
	"math"
	"os"
	"time"

	"github.com/lucas-clemente/quic-go/internal/protocol"
	"github.com/lucas-clemente/quic-go/internal/utils"
)

const (
	maxBurstBytes                                     = 3 * protocol.DefaultTCPMSS
	renoBeta                       float32            = 0.7 // Reno backoff factor.
	defaultMinimumCongestionWindow protocol.ByteCount = 2 * protocol.DefaultTCPMSS
	rtt0                           uint32             = 25
)

type hyblaSender struct {
	hybridSlowStart HybridSlowStart
	prr             PrrSender
	rttStats        *RTTStats
	stats           connectionStats
	hybla           *Hybla
	p               uint32
	firstRun        uint32
	ssCounter       uint32

	noPRR bool
	reno  bool

	// Track the largest packet that has been sent.
	largestSentPacketNumber protocol.PacketNumber

	// Track the largest packet that has been acked.
	largestAckedPacketNumber protocol.PacketNumber

	// Track the largest packet number outstanding when a CWND cutback occurs.
	largestSentAtLastCutback protocol.PacketNumber

	// Whether the last loss event caused us to exit slowstart.
	// Used for stats collection of slowstartPacketsLost
	lastCutbackExitedSlowstart bool

	// When true, exit slow start with large cutback of congestion window.
	slowStartLargeReduction bool

	// Congestion window in packets.
	congestionWindow protocol.ByteCount

	// Minimum congestion window in packets.
	minCongestionWindow protocol.ByteCount

	// Maximum congestion window.
	maxCongestionWindow protocol.ByteCount

	// Slow start congestion window in bytes, aka ssthresh.
	slowstartThreshold protocol.ByteCount

	// Number of connections to simulate.
	numConnections int

	// ACK counter for the Reno implementation.
	numAckedPackets uint64

	initialCongestionWindow    protocol.ByteCount
	initialMaxCongestionWindow protocol.ByteCount
	minSlowStartExitWindow     protocol.ByteCount
}

var _ SendAlgorithm = &hyblaSender{}
var _ SendAlgorithmWithDebugInfos = &hyblaSender{}

// RecordCongestionWindow records the congestion window and its timestamp.
func (h *hyblaSender) RecordCongestionWindow() {

	// Construct the file path for the "save.txt" file in the home directory
	filePath := "/test_container/save.txt"

	// Open the file in append mode, creating it if it doesn't exist
	file, err := os.OpenFile(filePath, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0644)
	if err != nil {
		log.Println("Error opening file:", err)
		return
	}
	defer file.Close()

	// Create a writer to append content to the file
	writer := bufio.NewWriter(file)

	// Write the congestion window size to the file
	_, err = fmt.Fprintf(writer, "\nSize of cwnd at moment %s is %d\n", time.Now().Format("2006-01-02 15:04:05"), int(h.congestionWindow))
	if err != nil {
		log.Println("Error writing to file:", err)
		return
	}
	// Flush the writer to ensure all buffered data is written to the file
	err = writer.Flush()
	if err != nil {
		log.Println("Error flushing writer:", err)
		return
	}
	log.Println("Congestion window size recorded in the file successfully.")
}

// RecordCongestionWindow records the congestion window and its timestamp.
func (h *hyblaSender) RecordPacketLoss() {

	// Construct the file path for the "save.txt" file in the home directory
	filePath := "/test_container/save.txt"

	// Open the file in append mode, creating it if it doesn't exist
	file, err := os.OpenFile(filePath, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0644)
	if err != nil {
		log.Println("Error opening file:", err)
		return
	}
	defer file.Close()

	// Create a writer to append content to the file
	writer := bufio.NewWriter(file)

	// Write the congestion window size to the file
	_, err = fmt.Fprintf(writer, "YOU HAVE REACHED SOME PACKET LOSS HERE\n")
	if err != nil {
		log.Println("Error writing to file:", err)
		return
	}
	// Flush the writer to ensure all buffered data is written to the file
	err = writer.Flush()
	if err != nil {
		log.Println("Error flushing writer:", err)
		return
	}
	log.Println("Congestion window size recorded in the file successfully.")
}

// NewHyblaSender makes a new hybla sender
func NewHyblaSender(clock Clock, rttStats *RTTStats, reno bool, initialCongestionWindow, initialMaxCongestionWindow protocol.ByteCount) *hyblaSender {
	return &hyblaSender{
		rttStats:                   rttStats,
		largestSentPacketNumber:    protocol.InvalidPacketNumber,
		largestAckedPacketNumber:   protocol.InvalidPacketNumber,
		largestSentAtLastCutback:   protocol.InvalidPacketNumber,
		initialCongestionWindow:    initialCongestionWindow,
		initialMaxCongestionWindow: initialMaxCongestionWindow,
		congestionWindow:           initialCongestionWindow,
		minCongestionWindow:        defaultMinimumCongestionWindow,
		slowstartThreshold:         initialMaxCongestionWindow,
		maxCongestionWindow:        initialMaxCongestionWindow,
		numConnections:             defaultNumConnections,
		hybla:                      NewHybla(clock),
		reno:                       reno,
		ssCounter:                  0,
		firstRun:                   1,
		p:                          1,
	}
}

// TimeUntilSend returns when the next packet should be sent.
func (h *hyblaSender) TimeUntilSend(bytesInFlight protocol.ByteCount) time.Duration {
	if !h.noPRR && h.InRecovery() {
		// PRR is used when in recovery.
		if h.prr.CanSend(h.GetCongestionWindow(), bytesInFlight, h.GetSlowStartThreshold()) {
			return 0
		}
	}
	return h.rttStats.SmoothedRTT() * time.Duration(protocol.DefaultTCPMSS) / time.Duration(2*h.GetCongestionWindow())
}

func (h *hyblaSender) calculateP() {
	h.p = uint32(h.rttStats.SmoothedRTT()) / (rtt0 * 1000 * 1000)
	//log.Printf("ms = %v, rtt = %d, referenceRTT: %d, p = %d",h.rttStats.SmoothedRTT(), uint32(h.rttStats.SmoothedRTT()), referenceRTT, h.p)
	if h.p < 1 {
		h.p = 1
	}
}

func (h *hyblaSender) OnPacketSent(
	sentTime time.Time,
	bytesInFlight protocol.ByteCount,
	packetNumber protocol.PacketNumber,
	bytes protocol.ByteCount,
	isRetransmittable bool,
) {
	if !isRetransmittable {
		return
	}
	if h.InRecovery() {
		// PRR is used when in recovery.
		h.prr.OnPacketSent(bytes)
	}
	h.largestSentPacketNumber = packetNumber
	h.hybridSlowStart.OnPacketSent(packetNumber)
}

func (h *hyblaSender) CanSend(bytesInFlight protocol.ByteCount) bool {
	if !h.noPRR && h.InRecovery() {
		return h.prr.CanSend(h.GetCongestionWindow(), bytesInFlight, h.GetSlowStartThreshold())
	}
	return bytesInFlight < h.GetCongestionWindow()
}

func (h *hyblaSender) InRecovery() bool {
	return h.largestAckedPacketNumber != protocol.InvalidPacketNumber && h.largestAckedPacketNumber <= h.largestSentAtLastCutback
}

func (h *hyblaSender) InSlowStart() bool {
	return h.GetCongestionWindow() < h.GetSlowStartThreshold()
}

func (h *hyblaSender) GetCongestionWindow() protocol.ByteCount {
	return h.congestionWindow
}

func (h *hyblaSender) GetSlowStartThreshold() protocol.ByteCount {
	return h.slowstartThreshold
}

func (h *hyblaSender) ExitSlowstart() {
	h.slowstartThreshold = h.congestionWindow
}

func (h *hyblaSender) SlowstartThreshold() protocol.ByteCount {
	return h.slowstartThreshold
}

func (h *hyblaSender) MaybeExitSlowStart() {
	//if h.InSlowStart() && h.hybridSlowStart.ShouldExitSlowStart(h.rttStats.LatestRTT(), h.rttStats.MinRTT(), h.GetCongestionWindow()/protocol.DefaultTCPMSS) {
	//	h.ExitSlowstart()
	///

	if h.InSlowStart() && (h.congestionWindow >= h.slowstartThreshold) {
		h.ExitSlowstart()
	}
}

func (h *hyblaSender) OnPacketAcked(
	ackedPacketNumber protocol.PacketNumber,
	ackedBytes protocol.ByteCount,
	priorInFlight protocol.ByteCount,
	eventTime time.Time,
) {
	if h.firstRun != 0 {
		h.calculateP()
		h.RecordCongestionWindow()
		h.slowstartThreshold = h.slowstartThreshold * protocol.ByteCount(h.p)
		h.maxCongestionWindow = h.maxCongestionWindow * protocol.ByteCount(h.p)
		h.initialMaxCongestionWindow = h.initialMaxCongestionWindow * protocol.ByteCount(h.p)
		h.initialCongestionWindow = h.initialCongestionWindow * protocol.ByteCount(h.p)
		h.firstRun = 0
	}
	h.largestAckedPacketNumber = utils.MaxPacketNumber(ackedPacketNumber, h.largestAckedPacketNumber)
	if h.InRecovery() {
		// PRR is used when in recovery.
		if !h.noPRR {
			h.prr.OnPacketAcked(ackedBytes)
		}
		return
	}
	h.maybeIncreaseCwnd(ackedPacketNumber, ackedBytes, priorInFlight, eventTime)
	if h.InSlowStart() {
		h.hybridSlowStart.OnPacketAcked(ackedPacketNumber)
	}
}

func (h *hyblaSender) OnPacketLost(
	packetNumber protocol.PacketNumber,
	lostBytes protocol.ByteCount,
	priorInFlight protocol.ByteCount,
) {
	h.RecordPacketLoss()
	log.Printf("LOST PACKET, congestionWindow: %d, ssthresh: %d\n", h.congestionWindow, h.slowstartThreshold)
	// TCP NewReno (RFC6582) says that once a loss occurs, any losses in packets
	// already sent should be treated as a single loss event, since it's expected.
	if packetNumber <= h.largestSentAtLastCutback {
		if h.lastCutbackExitedSlowstart {
			h.stats.slowstartPacketsLost++
			h.stats.slowstartBytesLost += lostBytes
			if h.slowStartLargeReduction {
				// Reduce congestion window by lost_bytes for every loss.
				h.congestionWindow = utils.MaxByteCount(h.congestionWindow-lostBytes, h.minSlowStartExitWindow)
				h.RecordCongestionWindow()
				h.slowstartThreshold = h.congestionWindow
			}
		}
		return
	}
	h.lastCutbackExitedSlowstart = h.InSlowStart()
	if h.InSlowStart() {
		h.stats.slowstartPacketsLost++
	}

	if !h.noPRR {
		h.prr.OnPacketLost(priorInFlight)
	}
	// TODO(chromium): Separate out all of slow start into a separate class.
	if h.slowStartLargeReduction && h.InSlowStart() {
		if h.congestionWindow >= 2*h.initialCongestionWindow {
			h.minSlowStartExitWindow = h.congestionWindow / 2
		}
		h.congestionWindow -= protocol.DefaultTCPMSS
	} else if h.reno {
		h.congestionWindow = protocol.ByteCount(float32(h.congestionWindow) * h.RenoBeta())
	} else {
		h.congestionWindow = h.hybla.CongestionWindowAfterPacketLoss(h.congestionWindow)
	}
	if h.congestionWindow < h.minCongestionWindow {
		h.congestionWindow = h.minCongestionWindow
	}
	h.RecordCongestionWindow()
	h.slowstartThreshold = h.congestionWindow
	h.largestSentAtLastCutback = h.largestSentPacketNumber
	// reset packet count from congestion avoidance mode. We start
	// counting again when we're out of recovery.
	h.numAckedPackets = 0
	//log.Printf("congestion = %d", h.congestionWindow)
}

func (h *hyblaSender) RenoBeta() float32 {
	// kNConnectionBeta is the backoff factor after loss for our N-connection
	// emulation, which emulates the effective backoff of an ensemble of N
	// TCP-Reno connections on a single loss event. The effective multiplier is
	// computed as:
	return (float32(h.numConnections) - 1. + renoBeta) / float32(h.numConnections)
}

// Called when we receive an ack. Normal TCP tracks how many packets one ack
// represents, but quic has a separate ack for each packet.
func (h *hyblaSender) maybeIncreaseCwnd(
	_ protocol.PacketNumber,
	ackedBytes protocol.ByteCount,
	priorInFlight protocol.ByteCount,
	eventTime time.Time,
) {
	// Do not increase the congestion window unless the sender is close to using
	// the current window.
	if !h.isCwndLimited(priorInFlight) {
		log.Printf("LIMITED: %d, thresh %d, infli: %d, bandwidth: %d, rtt: %d\n", h.congestionWindow, h.slowstartThreshold, priorInFlight, h.BandwidthEstimate(), h.rttStats.SmoothedRTT())
		h.hybla.OnApplicationLimited()
		h.RecordCongestionWindow()
		return
	}
	if h.congestionWindow >= h.maxCongestionWindow {
		return
	}
	if h.InSlowStart() {
		//log.Printf("pnum: %d, ssthresh: %d, h.congestionWindow: %d, ssCounter: %d, inFlight = %d\n", h.p, h.slowstartThreshold, h.congestionWindow, h.ssCounter, priorInFlight)
		// TCP slow start, exponential growth, increase by one for each ACK.
		h.congestionWindow += protocol.ByteCount(uint32(protocol.DefaultTCPMSS) * (uint32(math.Pow(2, float64(h.p))) - 1))
		//h.congestionWindow = protocol.ByteCount(int(h.congestionWindow) * int(h.p))
		log.Printf("AFTER SS: %d, thresh %d, infli: %d, bandwidth: %d, rtt: %d\n", h.congestionWindow, h.slowstartThreshold, priorInFlight, h.BandwidthEstimate(), h.rttStats.SmoothedRTT())
		h.RecordCongestionWindow()
		return
	}
	// Congestion avoidance
	if h.reno {
		// Classic Reno congestion avoidance.
		h.numAckedPackets++
		// Divide by num_connections to smoothly increase the CWND at a faster
		// rate than conventional Reno.

		if h.numAckedPackets*uint64(h.numConnections) >= uint64(h.congestionWindow)/uint64(protocol.DefaultTCPMSS) {
			h.congestionWindow += protocol.ByteCount(uint32(protocol.DefaultTCPMSS) * h.p)
			h.numAckedPackets = 0
		}
	} else {
		//log.Println("ELSE\n")
		//log.Printf("pnum: %d, ssthresh: %d, h.congestionWindow: %d, ssCounter: %d, inFlight = %d\n", h.p, h.slowstartThreshold, h.congestionWindow, h.ssCounter, priorInFlight)
		h.congestionWindow = utils.MinByteCount(h.maxCongestionWindow, protocol.ByteCount(uint32(h.hybla.CongestionWindowAfterAck(ackedBytes, h.congestionWindow, h.rttStats.MinRTT(), eventTime, h.p))))
		log.Printf("AFTER CA: %d, thresh %d, infli: %d, bandwidth: %d, rtt: %d\n", h.congestionWindow, h.slowstartThreshold, priorInFlight, h.BandwidthEstimate(), h.rttStats.SmoothedRTT())
		h.RecordCongestionWindow()
	}
}

func (h *hyblaSender) isCwndLimited(bytesInFlight protocol.ByteCount) bool {
	congestionWindow := h.GetCongestionWindow()
	if bytesInFlight >= congestionWindow {
		return true
	}
	availableBytes := congestionWindow - bytesInFlight
	slowStartLimited := h.InSlowStart() && bytesInFlight > congestionWindow/2
	return slowStartLimited || availableBytes <= protocol.ByteCount(uint32(maxBurstBytes)*h.p)
}

// BandwidthEstimate returns the current bandwidth estimate
func (h *hyblaSender) BandwidthEstimate() Bandwidth {
	srtt := h.rttStats.SmoothedRTT()
	if srtt == 0 {
		// If we haven't measured an rtt, the bandwidth estimate is unknown.
		return 0
	}
	return BandwidthFromDelta(h.GetCongestionWindow(), srtt)
}

// HybridSlowStart returns the hybrid slow start instance for testing
func (h *hyblaSender) HybridSlowStart() *HybridSlowStart {
	return &h.hybridSlowStart
}

// SetNumEmulatedConnections sets the number of emulated connections
func (h *hyblaSender) SetNumEmulatedConnections(n int) {
	h.numConnections = utils.Max(n, 1)
	h.hybla.SetNumConnections(h.numConnections)
}

// OnRetransmissionTimeout is called on an retransmission timeout
func (h *hyblaSender) OnRetransmissionTimeout(packetsRetransmitted bool) {
	h.largestSentAtLastCutback = protocol.InvalidPacketNumber
	if !packetsRetransmitted {
		return
	}
	h.hybridSlowStart.Restart()
	h.hybla.Reset()
	h.slowstartThreshold = h.congestionWindow / 2
	h.congestionWindow = h.minCongestionWindow
}

// OnConnectionMigration is called when the connection is migrated (?)
func (h *hyblaSender) OnConnectionMigration() {
	h.hybridSlowStart.Restart()
	h.prr = PrrSender{}
	h.largestSentPacketNumber = protocol.InvalidPacketNumber
	h.largestAckedPacketNumber = protocol.InvalidPacketNumber
	h.largestSentAtLastCutback = protocol.InvalidPacketNumber
	h.lastCutbackExitedSlowstart = false
	h.hybla.Reset()
	h.numAckedPackets = 0
	h.congestionWindow = h.initialCongestionWindow
	h.slowstartThreshold = h.initialMaxCongestionWindow
	h.maxCongestionWindow = h.initialMaxCongestionWindow
}

// SetSlowStartLargeReduction allows enabling the SSLR experiment
func (h *hyblaSender) SetSlowStartLargeReduction(enabled bool) {
	h.slowStartLargeReduction = enabled
}

/* package congestion

import (
	"bufio"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/lucas-clemente/quic-go/internal/protocol"
	"github.com/lucas-clemente/quic-go/internal/utils"
)

const (
	maxBurstBytes                                     = 3 * protocol.DefaultTCPMSS
	renoBeta                       float32            = 0.7 // Reno backoff factor.
	defaultMinimumCongestionWindow protocol.ByteCount = 2 * protocol.DefaultTCPMSS
	ssOffset                       int                = 50
)

type cubicSender struct {
	hybridSlowStart HybridSlowStart
	prr             PrrSender
	rttStats        *RTTStats
	stats           connectionStats
	cubic           *Cubic

	noPRR bool
	reno  bool

	// Track the largest packet that has been sent.
	largestSentPacketNumber protocol.PacketNumber

	// Track the largest packet that has been acked.
	largestAckedPacketNumber protocol.PacketNumber

	// Track the largest packet number outstanding when a CWND cutback occurs.
	largestSentAtLastCutback protocol.PacketNumber

	// Whether the last loss event caused us to exit slowstart.
	// Used for stats collection of slowstartPacketsLost
	lastCutbackExitedSlowstart bool

	// When true, exit slow start with large cutback of congestion window.
	slowStartLargeReduction bool

	// Congestion window in packets.
	congestionWindow protocol.ByteCount

	// Minimum congestion window in packets.
	minCongestionWindow protocol.ByteCount

	// Maximum congestion window.
	maxCongestionWindow protocol.ByteCount

	// Slow start congestion window in bytes, aka ssthresh.
	slowstartThreshold protocol.ByteCount

	// Number of connections to simulate.
	numConnections int

	// ACK counter for the Reno implementation.
	numAckedPackets uint64

	initialCongestionWindow    protocol.ByteCount
	initialMaxCongestionWindow protocol.ByteCount

	minSlowStartExitWindow protocol.ByteCount
	previousRTT            time.Duration
}

var _ SendAlgorithm = &cubicSender{}
var _ SendAlgorithmWithDebugInfos = &cubicSender{}

// NewCubicSender makes a new cubic sender
func NewCubicSender(clock Clock, rttStats *RTTStats, reno bool, initialCongestionWindow, initialMaxCongestionWindow protocol.ByteCount) *cubicSender {
	return &cubicSender{
		rttStats:                   rttStats,
		largestSentPacketNumber:    protocol.InvalidPacketNumber,
		largestAckedPacketNumber:   protocol.InvalidPacketNumber,
		largestSentAtLastCutback:   protocol.InvalidPacketNumber,
		initialCongestionWindow:    initialCongestionWindow,
		initialMaxCongestionWindow: initialMaxCongestionWindow,
		congestionWindow:           initialCongestionWindow,
		minCongestionWindow:        defaultMinimumCongestionWindow,
		slowstartThreshold:         initialMaxCongestionWindow,
		maxCongestionWindow:        initialMaxCongestionWindow,
		numConnections:             defaultNumConnections,
		cubic:                      NewCubic(clock),
		reno:                       reno,
	}
}

// TimeUntilSend returns when the next packet should be sent.
func (c *cubicSender) TimeUntilSend(bytesInFlight protocol.ByteCount) time.Duration {
	if !c.noPRR && c.InRecovery() {
		// PRR is used when in recovery.
		if c.prr.CanSend(c.GetCongestionWindow(), bytesInFlight, c.GetSlowStartThreshold()) {
			return 0
		}
	}
	return c.rttStats.SmoothedRTT() * time.Duration(protocol.DefaultTCPMSS) / time.Duration(2*c.GetCongestionWindow())
}

func (c *cubicSender) OnPacketSent(
	sentTime time.Time,
	bytesInFlight protocol.ByteCount,
	packetNumber protocol.PacketNumber,
	bytes protocol.ByteCount,
	isRetransmittable bool,
) {
	if !isRetransmittable {
		return
	}
	if c.InRecovery() {
		// PRR is used when in recovery.
		c.prr.OnPacketSent(bytes)
	}
	c.largestSentPacketNumber = packetNumber
	c.hybridSlowStart.OnPacketSent(packetNumber)
}

// RecordCongestionWindow records the congestion window and its timestamp.
func (c *cubicSender) RecordCongestionWindow() {

	// Construct the file path for the "save.txt" file in the home directory
	filePath := "/test_container/save.txt"

	// Open the file in append mode, creating it if it doesn't exist
	file, err := os.OpenFile(filePath, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0644)
	if err != nil {
		log.Println("Error opening file:", err)
		return
	}
	defer file.Close()

	// Create a writer to append content to the file
	writer := bufio.NewWriter(file)

	// Write the congestion window size to the file
	_, err = fmt.Fprintf(writer, "\nSize of cwnd at moment %s is %d\n", time.Now().Format("2006-01-02 15:04:05"), int(c.congestionWindow))
	if err != nil {
		log.Println("Error writing to file:", err)
		return
	}
	// Flush the writer to ensure all buffered data is written to the file
	err = writer.Flush()
	if err != nil {
		log.Println("Error flushing writer:", err)
		return
	}
	log.Println("Congestion window size recorded in the file successfully.")
}

// RecordCongestionWindow records the congestion window and its timestamp.
func (c *cubicSender) RecordPacketLoss() {

	// Construct the file path for the "save.txt" file in the home directory
	filePath := "/test_container/save.txt"

	// Open the file in append mode, creating it if it doesn't exist
	file, err := os.OpenFile(filePath, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0644)
	if err != nil {
		log.Println("Error opening file:", err)
		return
	}
	defer file.Close()

	// Create a writer to append content to the file
	writer := bufio.NewWriter(file)

	// Write the congestion window size to the file
	_, err = fmt.Fprintf(writer, "RECORDED SOME PACKET LOSS RECORDED SOME PACKET LOSS\n")
	if err != nil {
		log.Println("Error writing to file:", err)
		return
	}
	// Flush the writer to ensure all buffered data is written to the file
	err = writer.Flush()
	if err != nil {
		log.Println("Error flushing writer:", err)
		return
	}
	log.Println("PACKED LOSS RECORDED recorded in the file successfully.\n")
}

func (c *cubicSender) CanSend(bytesInFlight protocol.ByteCount) bool {
	if !c.noPRR && c.InRecovery() {
		return c.prr.CanSend(c.GetCongestionWindow(), bytesInFlight, c.GetSlowStartThreshold())
	}
	return bytesInFlight < c.GetCongestionWindow()
}

func (c *cubicSender) InRecovery() bool {
	return c.largestAckedPacketNumber != protocol.InvalidPacketNumber && c.largestAckedPacketNumber <= c.largestSentAtLastCutback
}

func (c *cubicSender) InSlowStart() bool {
	log.Printf("HELLO HELLO PRINTING : %d %d\n", c.rttStats.SmoothedRTT()/1000000, c.previousRTT/1000000)
	return ((c.previousRTT == 0) || ((int(c.rttStats.SmoothedRTT()) / 1000000) < ((int(c.previousRTT) / 1000000) + ssOffset)))
	//return c.GetCongestionWindow() < c.GetSlowStartThreshold()
}

func (c *cubicSender) GetCongestionWindow() protocol.ByteCount {
	return c.congestionWindow
}

func (c *cubicSender) GetSlowStartThreshold() protocol.ByteCount {
	return c.slowstartThreshold
}

func (c *cubicSender) ExitSlowstart() {
	c.slowstartThreshold = c.congestionWindow
}

func (c *cubicSender) SlowstartThreshold() protocol.ByteCount {
	return c.slowstartThreshold
}

func (c *cubicSender) MaybeExitSlowStart() {
	if c.InSlowStart() && c.hybridSlowStart.ShouldExitSlowStart(c.rttStats.LatestRTT(), c.rttStats.MinRTT(), c.GetCongestionWindow()/protocol.DefaultTCPMSS) {
		c.ExitSlowstart()
	}
}

func (c *cubicSender) OnPacketAcked(
	ackedPacketNumber protocol.PacketNumber,
	ackedBytes protocol.ByteCount,
	priorInFlight protocol.ByteCount,
	eventTime time.Time,
) {
	c.largestAckedPacketNumber = utils.MaxPacketNumber(ackedPacketNumber, c.largestAckedPacketNumber)
	if c.InRecovery() {
		// PRR is used when in recovery.
		if !c.noPRR {
			c.prr.OnPacketAcked(ackedBytes)
		}
		return
	}
	c.maybeIncreaseCwnd(ackedPacketNumber, ackedBytes, priorInFlight, eventTime)
	if c.InSlowStart() {
		c.hybridSlowStart.OnPacketAcked(ackedPacketNumber)
	}
}

func (c *cubicSender) OnPacketLost(
	packetNumber protocol.PacketNumber,
	lostBytes protocol.ByteCount,
	priorInFlight protocol.ByteCount,
) {
	// TCP NewReno (RFC6582) says that once a loss occurs, any losses in packets
	// already sent should be treated as a single loss event, since it's expected.
	c.RecordPacketLoss()
	if packetNumber <= c.largestSentAtLastCutback {
		if c.lastCutbackExitedSlowstart {
			c.stats.slowstartPacketsLost++
			c.stats.slowstartBytesLost += lostBytes
			if c.slowStartLargeReduction {
				// Reduce congestion window by lost_bytes for every loss.
				c.congestionWindow = utils.MaxByteCount(c.congestionWindow-lostBytes, c.minSlowStartExitWindow)
				c.slowstartThreshold = c.congestionWindow
				c.RecordCongestionWindow()
			}
		}
		return
	}
	c.lastCutbackExitedSlowstart = c.InSlowStart()
	if c.InSlowStart() {
		c.stats.slowstartPacketsLost++
	}

	if !c.noPRR {
		c.prr.OnPacketLost(priorInFlight)
	}

	// TODO(chromium): Separate out all of slow start into a separate class.
	if c.slowStartLargeReduction && c.InSlowStart() {
		if c.congestionWindow >= 2*c.initialCongestionWindow {
			c.minSlowStartExitWindow = c.congestionWindow / 2
		}
		c.congestionWindow -= protocol.DefaultTCPMSS
	} else if c.reno {
		c.congestionWindow = protocol.ByteCount(float32(c.congestionWindow) * c.RenoBeta())
	} else {
		c.congestionWindow = c.cubic.CongestionWindowAfterPacketLoss(c.congestionWindow)
	}
	if c.congestionWindow < c.minCongestionWindow {
		c.congestionWindow = c.minCongestionWindow
	}
	c.slowstartThreshold = c.congestionWindow
	c.largestSentAtLastCutback = c.largestSentPacketNumber
	c.RecordCongestionWindow()
	// reset packet count from congestion avoidance mode. We start
	// counting again when we're out of recovery.
	c.numAckedPackets = 0
}

func (c *cubicSender) RenoBeta() float32 {
	// kNConnectionBeta is the backoff factor after loss for our N-connection
	// emulation, which emulates the effective backoff of an ensemble of N
	// TCP-Reno connections on a single loss event. The effective multiplier is
	// computed as:
	return (float32(c.numConnections) - 1. + renoBeta) / float32(c.numConnections)
}

// Called when we receive an ack. Normal TCP tracks how many packets one ack
// represents, but quic has a separate ack for each packet.
func (c *cubicSender) maybeIncreaseCwnd(
	_ protocol.PacketNumber,
	ackedBytes protocol.ByteCount,
	priorInFlight protocol.ByteCount,
	eventTime time.Time,
) {
	// Do not increase the congestion window unless the sender is close to using
	// the current window.
	if !c.isCwndLimited(priorInFlight) {
		c.cubic.OnApplicationLimited()
		log.Printf("LIMITED: %d, thresh %d, infli: %d, bandwidth: %d, rtt: %d, lastrtt: %d\n", c.congestionWindow, c.slowstartThreshold, priorInFlight, c.BandwidthEstimate(), c.rttStats.SmoothedRTT()/1000000, c.previousRTT/1000000)
		return
	}
	if c.congestionWindow >= c.maxCongestionWindow {
		return
	}
	if c.InSlowStart() {
		// TCP slow start, exponential growth, increase by one for each ACK.
		c.RecordCongestionWindow()
		c.congestionWindow += protocol.DefaultTCPMSS
		log.Printf("AFTER SS: %d, thresh %d, infli: %d, bandwidth: %d, rtt: %d, lastrtt: %d\n", c.congestionWindow, c.slowstartThreshold, priorInFlight, c.BandwidthEstimate(), c.rttStats.SmoothedRTT()/1000000, c.previousRTT/1000000)
		c.previousRTT = c.rttStats.SmoothedRTT()
		return
	}
	// Congestion avoidance
	if c.reno {
		// Classic Reno congestion avoidance.
		c.numAckedPackets++
		// Divide by num_connections to smoothly increase the CWND at a faster
		// rate than conventional Reno.
		if c.numAckedPackets*uint64(c.numConnections) >= uint64(c.congestionWindow)/uint64(protocol.DefaultTCPMSS) {
			c.congestionWindow += protocol.DefaultTCPMSS
			c.numAckedPackets = 0
		}
	} else {
		c.RecordCongestionWindow()
		c.congestionWindow = utils.MinByteCount(c.maxCongestionWindow, c.cubic.CongestionWindowAfterAck(ackedBytes, c.congestionWindow, c.rttStats.MinRTT(), eventTime, c.rttStats.SmoothedRTT()))
		log.Printf("AFTER CA: %d, thresh %d, infli: %d, bandwidth: %d, rtt: %d, lastrtt: %d\n", c.congestionWindow, c.slowstartThreshold, priorInFlight, c.BandwidthEstimate(), c.rttStats.SmoothedRTT()/1000000, c.previousRTT/1000000)
		c.previousRTT = c.rttStats.SmoothedRTT()
	}
}

func (c *cubicSender) isCwndLimited(bytesInFlight protocol.ByteCount) bool {
	congestionWindow := c.GetCongestionWindow()
	if bytesInFlight >= congestionWindow {
		return true
	}
	availableBytes := congestionWindow - bytesInFlight
	slowStartLimited := c.InSlowStart() && bytesInFlight > congestionWindow/2
	return slowStartLimited || availableBytes <= maxBurstBytes
}

// BandwidthEstimate returns the current bandwidth estimate
func (c *cubicSender) BandwidthEstimate() Bandwidth {
	srtt := c.rttStats.SmoothedRTT()
	if srtt == 0 {
		// If we haven't measured an rtt, the bandwidth estimate is unknown.
		return 0
	}
	return BandwidthFromDelta(c.GetCongestionWindow(), srtt)
}

// HybridSlowStart returns the hybrid slow start instance for testing
func (c *cubicSender) HybridSlowStart() *HybridSlowStart {
	return &c.hybridSlowStart
}

// SetNumEmulatedConnections sets the number of emulated connections
func (c *cubicSender) SetNumEmulatedConnections(n int) {
	c.numConnections = utils.Max(n, 1)
	c.cubic.SetNumConnections(c.numConnections)
}

// OnRetransmissionTimeout is called on an retransmission timeout
func (c *cubicSender) OnRetransmissionTimeout(packetsRetransmitted bool) {
	c.largestSentAtLastCutback = protocol.InvalidPacketNumber
	if !packetsRetransmitted {
		return
	}
	c.hybridSlowStart.Restart()
	c.cubic.Reset()
	c.slowstartThreshold = c.congestionWindow / 2
	c.congestionWindow = c.minCongestionWindow
}

// OnConnectionMigration is called when the connection is migrated (?)
func (c *cubicSender) OnConnectionMigration() {
	c.hybridSlowStart.Restart()
	c.prr = PrrSender{}
	c.largestSentPacketNumber = protocol.InvalidPacketNumber
	c.largestAckedPacketNumber = protocol.InvalidPacketNumber
	c.largestSentAtLastCutback = protocol.InvalidPacketNumber
	c.lastCutbackExitedSlowstart = false
	c.cubic.Reset()
	c.numAckedPackets = 0
	c.congestionWindow = c.initialCongestionWindow
	c.slowstartThreshold = c.initialMaxCongestionWindow
	c.maxCongestionWindow = c.initialMaxCongestionWindow
}

// SetSlowStartLargeReduction allows enabling the SSLR experiment
func (c *cubicSender) SetSlowStartLargeReduction(enabled bool) {
	c.slowStartLargeReduction = enabled
} */

/* package congestion

import (
	"bufio"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/lucas-clemente/quic-go/internal/protocol"
	"github.com/lucas-clemente/quic-go/internal/utils"
)

const (
	maxBurstBytes                                     = 3 * protocol.DefaultTCPMSS
	defaultMinimumCongestionWindow protocol.ByteCount = 2 * protocol.DefaultTCPMSS
	ssOffset                       int                = 100
	ssThresh                       int                = 800
)

type BBRSender struct {
	rttStats  *RTTStats
	stats     connectionStats
	bbr       *BBR
	firstRun  uint32
	ssCounter uint32
	minrtt    int
	epoch     time.Time
	more      int
	less      int

	// Track the largest packet that has been sent.
	largestSentPacketNumber protocol.PacketNumber

	// Track the largest packet that has been acked.
	largestAckedPacketNumber protocol.PacketNumber

	// Track the largest packet number outstanding when a CWND cutback occurs.
	largestSentAtLastCutback protocol.PacketNumber

	// Whether the last loss event caused us to exit slowstart.
	// Used for stats collection of slowstartPacketsLost
	lastCutbackExitedSlowstart bool

	// Congestion window in packets.
	congestionWindow protocol.ByteCount

	// Minimum congestion window in packets.
	minCongestionWindow protocol.ByteCount

	// Maximum congestion window.
	maxCongestionWindow protocol.ByteCount

	// Number of connections to simulate.
	numConnections int

	// ACK counter for the Reno implementation.
	numAckedPackets     int
	ssFlag              int
	prevNumAckedPackets int

	initialCongestionWindow    protocol.ByteCount
	initialMaxCongestionWindow protocol.ByteCount
	minSlowStartExitWindow     protocol.ByteCount
	previousRTT                int
	increaseFlag               int
	epochCounter               int
}

var _ SendAlgorithm = &BBRSender{}
var _ SendAlgorithmWithDebugInfos = &BBRSender{}

// RecordCongestionWindow records the congestion window and its timestamp.
func (b *BBRSender) RecordCongestionWindow() {

	// Construct the file path for the "save.txt" file in the home directory
	filePath := "/test_container/save.txt"

	// Open the file in append mode, creating it if it doesn't exist
	file, err := os.OpenFile(filePath, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0644)
	if err != nil {
		log.Println("Error opening file:", err)
		return
	}
	defer file.Close()

	// Create a writer to append content to the file
	writer := bufio.NewWriter(file)

	// Write the congestion window size to the file
	_, err = fmt.Fprintf(writer, "\nSize of cwnd at moment %s is %d\n", time.Now().Format("2006-01-02 15:04:05"), int(b.congestionWindow))
	if err != nil {
		log.Println("Error writing to file:", err)
		return
	}
	// Flush the writer to ensure all buffered data is written to the file
	err = writer.Flush()
	if err != nil {
		log.Println("Error flushing writer:", err)
		return
	}
	//log.Println("Congestion window size recorded in the file successfully.")
}

// RecordCongestionWindow records the congestion window and its timestamp.
func (b *BBRSender) RecordPacketLoss() {

	// Construct the file path for the "save.txt" file in the home directory
	filePath := "/test_container/save.txt"

	// Open the file in append mode, creating it if it doesn't exist
	file, err := os.OpenFile(filePath, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0644)
	if err != nil {
		log.Println("Error opening file:", err)
		return
	}
	defer file.Close()

	// Create a writer to append content to the file
	writer := bufio.NewWriter(file)

	// Write the congestion window size to the file
	_, err = fmt.Fprintf(writer, "YOU HAVE REACHED SOME PACKET LOSS HERE\n")
	if err != nil {
		log.Println("Error writing to file:", err)
		return
	}
	// Flush the writer to ensure all buffered data is written to the file
	err = writer.Flush()
	if err != nil {
		log.Println("Error flushing writer:", err)
		return
	}
	//log.Println("Congestion window size recorded in the file successfully.")
}

// NewbbrSender makes a new bbr sender
func NewBBRSender(clock Clock, rttStats *RTTStats, reno bool, initialCongestionWindow, initialMaxCongestionWindow protocol.ByteCount) *BBRSender {
	return &BBRSender{
		rttStats:                   rttStats,
		largestSentPacketNumber:    protocol.InvalidPacketNumber,
		largestAckedPacketNumber:   protocol.InvalidPacketNumber,
		largestSentAtLastCutback:   protocol.InvalidPacketNumber,
		initialCongestionWindow:    initialCongestionWindow,
		initialMaxCongestionWindow: initialMaxCongestionWindow,
		congestionWindow:           initialCongestionWindow,
		minCongestionWindow:        defaultMinimumCongestionWindow,
		maxCongestionWindow:        initialMaxCongestionWindow,
		numConnections:             defaultNumConnections,
		bbr:                        NewBBR(clock),
		ssCounter:                  0,
		firstRun:                   1,
		previousRTT:                10000,
		increaseFlag:               0,
		numAckedPackets:            0,
		prevNumAckedPackets:        0,
		epochCounter:               0,
		ssFlag:                     1,
		more:                       0,
		less:                       0,
		epoch:                      time.Time{},
		minrtt:                     10000,
	}
}

func (b *BBRSender) rttHasIncreased() bool {
	if b.previousRTT == 10000 {
		return false
	}
	return ((int(b.rttStats.SmoothedRTT()/1000000) > (b.previousRTT + ssOffset)) || (b.previousRTT > ssThresh))
}

// TimeUntilSend returns when the next packet should be sent.
func (b *BBRSender) TimeUntilSend(bytesInFlight protocol.ByteCount) time.Duration {
	return b.rttStats.SmoothedRTT() * time.Duration(protocol.DefaultTCPMSS) / time.Duration(2*b.GetCongestionWindow())
}

func (b *BBRSender) OnPacketSent(
	sentTime time.Time,
	bytesInFlight protocol.ByteCount,
	packetNumber protocol.PacketNumber,
	bytes protocol.ByteCount,
	isRetransmittable bool,
) {
	if !isRetransmittable {
		return
	}
	b.largestSentPacketNumber = packetNumber
}

func (b *BBRSender) CanSend(bytesInFlight protocol.ByteCount) bool {
	return bytesInFlight < b.GetCongestionWindow()
}

func (b *BBRSender) InRecovery() bool {
	return b.largestAckedPacketNumber != protocol.InvalidPacketNumber && b.largestAckedPacketNumber <= b.largestSentAtLastCutback
}

func (b *BBRSender) InSlowStart() bool {
	if b.previousRTT == 10000 {
		return true
	}
	return b.previousRTT < ssThresh //((b.previousRTT == 10000) || ((int(b.rttStats.SmoothedRTT()) / 1000000) < ssThresh)) //((int(b.previousRTT) / 1000000) + ssOffset)))
}

func (b *BBRSender) GetCongestionWindow() protocol.ByteCount {
	return b.congestionWindow
}

func (b *BBRSender) MaybeExitSlowStart() {

}

func (b *BBRSender) OnPacketAcked(
	ackedPacketNumber protocol.PacketNumber,
	ackedBytes protocol.ByteCount,
	priorInFlight protocol.ByteCount,
	eventTime time.Time,
) {
	b.numAckedPackets += 1
	b.largestAckedPacketNumber = utils.MaxPacketNumber(ackedPacketNumber, b.largestAckedPacketNumber)
	if b.InRecovery() {
		return
	}
	b.maybeIncreaseCwnd(ackedPacketNumber, ackedBytes, priorInFlight, eventTime)
}

func (b *BBRSender) OnPacketLost(
	packetNumber protocol.PacketNumber,
	lostBytes protocol.ByteCount,
	priorInFlight protocol.ByteCount,
) {
	b.RecordPacketLoss()
	// TCP NewReno (RFC6582) says that once a loss occurs, any losses in packets
	// already sent should be treated as a single loss event, since it's expected.
	if packetNumber <= b.largestSentAtLastCutback {
		if b.lastCutbackExitedSlowstart {
			b.stats.slowstartPacketsLost++
			b.stats.slowstartBytesLost += lostBytes
		}
		return
	}
	b.lastCutbackExitedSlowstart = b.InSlowStart()
	if b.InSlowStart() {
		b.stats.slowstartPacketsLost++
	}

	//b.congestionWindow = b.bbr.CongestionWindowAfterPacketLoss(b.congestionWindow)
	b.RecordCongestionWindow()
	b.largestSentAtLastCutback = b.largestSentPacketNumber
	// reset packet count from congestion avoidance mode. We start
	// counting again when we're out of recovery.
	// b.numAckedPackets = 0
}

func (b *BBRSender) epochCheck() {
	if b.increaseFlag == 1 {
		b.increaseFlag = 0
		b.prevNumAckedPackets = b.numAckedPackets
		b.numAckedPackets = 0
	}
	if b.epochCounter >= 10 {
		log.Printf("END OF EPOCH\nEND OF EPOCH\nEND OF EPOCH\n Minimum over rtt: %d\n", b.rttStats.MinRTT())
		b.epochCounter = 0
		if !b.rttHasIncreased() {
			b.congestionWindow = protocol.ByteCount(float32(b.congestionWindow) * 1.25)
			b.congestionWindow = utils.MinByteCount(b.maxCongestionWindow, b.congestionWindow)
			b.more = 2
		}
		b.increaseFlag = 1
	}
}

// Called when we receive an ack. Normal TCP tracks how many packets one ack
// represents, but quic has a separate ack for each packet.
func (b *BBRSender) maybeIncreaseCwnd(
	_ protocol.PacketNumber,
	ackedBytes protocol.ByteCount,
	priorInFlight protocol.ByteCount,
	eventTime time.Time,
) {
	currentTime := time.Now()
	timeSinceEpoch := currentTime.Sub(b.epoch)
	if b.minrtt > (int(b.rttStats.SmoothedRTT()) / 1000000) {
		b.minrtt = int(b.rttStats.SmoothedRTT()) / 1000000
	}
	if (int(timeSinceEpoch) / 1000000) > b.previousRTT {
		/* if b.less == 2 {
			b.congestionWindow = protocol.ByteCount(float32(b.congestionWindow) * 1.18)
			b.congestionWindow = utils.MinByteCount(b.maxCongestionWindow, b.congestionWindow)
		} */ /*
		//log.Printf("Epoch ENDED: %s, currentTime: %s, timeSinceEpoch: %d\n", b.epoch, currentTime, int(timeSinceEpoch)/1000000)
		b.epoch = currentTime
		b.epochCounter += 1
		//log.Printf("Min: %d, Smooth: %d, timeSince: %d\n", b.minrtt, b.rttStats.SmoothedRTT()/1000000, timeSinceEpoch/1000000)
		b.previousRTT = b.minrtt
		b.minrtt = 10000
		//b.more = 0
		//b.less = 0
	}
	/* if b.more == 2 || b.less == 2 {
		log.Printf("MORE/LESS: %d, infli: %d, bandwidth: %d, rtt: %d, timeSinceEpoch: %d, minrtt: %d, previousRTT: %d\n", b.congestionWindow, priorInFlight, b.BandwidthEstimate(), int(b.rttStats.SmoothedRTT())/1000000, int(timeSinceEpoch)/1000000, b.minrtt, b.previousRTT)
		b.RecordCongestionWindow()
		return
	} */ /*
	// Do not increase the congestion window unless the sender is close to using
	// the current window.
	if !b.isCwndLimited(priorInFlight) {
		log.Printf("LIMIT: %d, infli: %d, bandwidth: %d, rtt: %d, timeSinceEpoch: %d, minrtt: %d, previousRTT: %d\n", b.congestionWindow, priorInFlight, b.BandwidthEstimate(), int(b.rttStats.SmoothedRTT())/1000000, int(timeSinceEpoch)/1000000, b.minrtt, b.previousRTT)
		//log.Printf("pnum: %d, ssthresh: %d, b.congestionWindow: %d, ssCounter: %d, inFlight = %d\n", b.p, b.slowstartThreshold, b.congestionWindow, b.ssCounter, priorInFlight)
		if b.ssFlag != 2 {
			b.RecordCongestionWindow()
			return
		}
		if b.increaseFlag == 1 && b.rttHasIncreased() == true && b.ssFlag == 2 {
			b.congestionWindow = utils.MinByteCount(b.maxCongestionWindow, b.congestionWindow)
			b.congestionWindow = utils.MinByteCount(b.maxCongestionWindow, protocol.ByteCount(int((float32(b.congestionWindow) * 0.8))))
			b.less = 2
			b.epochCheck()
		} else {
			b.epochCheck()
		}
		b.RecordCongestionWindow()
		return
	}
	if b.InSlowStart() && b.ssFlag != 2 {
		// TCP slow start, exponential growth, increase by one for each ACK.
		b.congestionWindow += protocol.DefaultTCPMSS
		log.Printf("SS: %d, infli: %d, bandwidth: %d, rtt: %d, timeSinceEpoch: %d, minrtt: %d, previousRTT: %d\n", b.congestionWindow, priorInFlight, b.BandwidthEstimate(), int(b.rttStats.SmoothedRTT())/1000000, int(timeSinceEpoch)/1000000, b.minrtt, b.previousRTT)
		b.RecordCongestionWindow()
		return
	} else {
		b.ssFlag = 2
		if b.increaseFlag == 1 && b.rttHasIncreased() == true {
			//b.congestionWindow = b.bbr.CongestionWindowAfterAck(ackedBytes, b.congestionWindow, eventTime, b.BandwidthEstimate())
			b.congestionWindow = utils.MinByteCount(b.maxCongestionWindow, b.congestionWindow)
			b.congestionWindow = protocol.ByteCount(int(float32(b.congestionWindow) * 0.8))
			//b.less = 2
		} else {
			//log.Printf("pnum: %d, ssthresh: %d, b.congestionWindow: %d, ssCounter: %d, inFlight = %d\n", b.p, h.slowstartThreshold, h.congestionWindow, h.ssCounter, priorInFlight)
			//b.congestionWindow = b.bbr.CongestionWindowAfterAck(ackedBytes, b.congestionWindow, eventTime, b.BandwidthEstimate())
			b.congestionWindow = utils.MinByteCount(b.maxCongestionWindow, b.congestionWindow)

		}
		log.Printf("CA: %d, infli: %d, bandwidth: %d, rtt: %d, timeSinceEpoch: %d, minrtt: %d, previousRTT: %d\n", b.congestionWindow, priorInFlight, b.BandwidthEstimate(), int(b.rttStats.SmoothedRTT())/1000000, int(timeSinceEpoch)/1000000, b.minrtt, b.previousRTT)
		b.epochCheck()
		b.RecordCongestionWindow()
	}
}

func (b *BBRSender) isCwndLimited(bytesInFlight protocol.ByteCount) bool {
	congestionWindow := b.GetCongestionWindow()
	if bytesInFlight >= congestionWindow {
		return true
	}
	availableBytes := congestionWindow - bytesInFlight

	return false || availableBytes <= protocol.ByteCount(uint32(maxBurstBytes))
}

// BandwidthEstimate returns the current bandwidth estimate
func (b *BBRSender) BandwidthEstimate() protocol.ByteCount {
	/* srtt := b.rttStats.SmoothedRTT()
	if srtt == 0 {
		// If we haven't measured an rtt, the bandwidth estimate is unknown.
		return 0
	}
	if BandwidthFromDelta(b.GetCongestionWindow(), srtt) > Bandwidth(b.minCongestionWindow) {
		return protocol.ByteCount(BandwidthFromDelta(b.GetCongestionWindow(), srtt))
	} else {
		return b.minCongestionWindow
	} */ /*
	srtt := b.rttStats.MinRTT()
	if srtt == 0 {
		return 0
	}
	estimate := b.prevNumAckedPackets * int(protocol.DefaultTCPMSS)
	if estimate < int(b.minCongestionWindow) {
		return b.minCongestionWindow
	}
	return protocol.ByteCount(estimate)
}

// SetNumEmulatedConnections sets the number of emulated connections
func (b *BBRSender) SetNumEmulatedConnections(n int) {
	b.numConnections = utils.Max(n, 1)
	b.bbr.SetNumConnections(b.numConnections)
}

// OnRetransmissionTimeout is called on an retransmission timeout
func (b *BBRSender) OnRetransmissionTimeout(packetsRetransmitted bool) {
	b.largestSentAtLastCutback = protocol.InvalidPacketNumber
	if !packetsRetransmitted {
		return
	}
	b.bbr.Reset()
	b.congestionWindow = protocol.ByteCount(b.BandwidthEstimate()) //b.minCongestionWindow
}

// OnConnectionMigration is called when the connection is migrated (?)
func (b *BBRSender) OnConnectionMigration() {
	b.largestSentPacketNumber = protocol.InvalidPacketNumber
	b.largestAckedPacketNumber = protocol.InvalidPacketNumber
	b.largestSentAtLastCutback = protocol.InvalidPacketNumber
	b.lastCutbackExitedSlowstart = false
	b.bbr.Reset()
	b.numAckedPackets = 0
	//b.congestionWindow = b.initialCongestionWindow
	b.maxCongestionWindow = b.initialMaxCongestionWindow
} */
