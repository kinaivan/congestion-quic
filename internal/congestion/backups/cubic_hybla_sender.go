/* package congestion

import (
	"log"
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

type cubicSender struct {
	hybridSlowStart HybridSlowStart
	prr             PrrSender
	rttStats        *RTTStats
	stats           connectionStats
	cubic           *Cubic
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

var _ SendAlgorithm = &cubicSender{}
var _ SendAlgorithmWithDebugInfos = &cubicSender{}

// RecordCongestionWindow records the congestion window and its timestamp.
func (h *cubicSender) RecordCongestionWindow() {
	log.Printf("Size of cwnd at moment %s is %d\n", time.Now().Format("2006-01-02 15:04:05"), int(h.congestionWindow))
}

// NewcubicSender makes a new hybla sender
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
		ssCounter:                  0,
		firstRun:                   1,
		p:                          1,
	}
}

// TimeUntilSend returns when the next packet should be sent.
func (h *cubicSender) TimeUntilSend(bytesInFlight protocol.ByteCount) time.Duration {
	if !h.noPRR && h.InRecovery() {
		// PRR is used when in recovery.
		if h.prr.CanSend(h.GetCongestionWindow(), bytesInFlight, h.GetSlowStartThreshold()) {
			return 0
		}
	}
	return h.rttStats.SmoothedRTT() * time.Duration(protocol.DefaultTCPMSS) / time.Duration(2*h.GetCongestionWindow())
}

func (h *cubicSender) calculateP() {
	h.p = uint32(h.rttStats.SmoothedRTT()) / (rtt0 * 1000 * 1000)
	//log.Printf("ms = %v, rtt = %d, referenceRTT: %d, p = %d",h.rttStats.SmoothedRTT(), uint32(h.rttStats.SmoothedRTT()), referenceRTT, h.p)
	if h.p < 1 {
		h.p = 1
	}
}

func (h *cubicSender) OnPacketSent(
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

func (h *cubicSender) CanSend(bytesInFlight protocol.ByteCount) bool {
	if !h.noPRR && h.InRecovery() {
		return h.prr.CanSend(h.GetCongestionWindow(), bytesInFlight, h.GetSlowStartThreshold())
	}
	return bytesInFlight < h.GetCongestionWindow()
}

func (h *cubicSender) InRecovery() bool {
	return h.largestAckedPacketNumber != protocol.InvalidPacketNumber && h.largestAckedPacketNumber <= h.largestSentAtLastCutback
}

func (h *cubicSender) InSlowStart() bool {
	return h.GetCongestionWindow() < h.GetSlowStartThreshold()
}

func (h *cubicSender) GetCongestionWindow() protocol.ByteCount {
	return h.congestionWindow
}

func (h *cubicSender) GetSlowStartThreshold() protocol.ByteCount {
	return h.slowstartThreshold
}

func (h *cubicSender) ExitSlowstart() {
	h.slowstartThreshold = h.congestionWindow
}

func (h *cubicSender) SlowstartThreshold() protocol.ByteCount {
	return h.slowstartThreshold
}

func (h *cubicSender) MaybeExitSlowStart() {
	/* if h.InSlowStart() && h.hybridSlowStart.ShouldExitSlowStart(h.rttStats.LatestRTT(), h.rttStats.MinRTT(), h.GetCongestionWindow()/protocol.DefaultTCPMSS) {
		h.ExitSlowstart()
	}

	if h.InSlowStart() && (h.congestionWindow >= h.slowstartThreshold) {
		h.ExitSlowstart()
	}
}

func (h *cubicSender) OnPacketAcked(
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

func (h *cubicSender) OnPacketLost(
	packetNumber protocol.PacketNumber,
	lostBytes protocol.ByteCount,
	priorInFlight protocol.ByteCount,
) {
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
		h.congestionWindow = h.cubic.CongestionWindowAfterPacketLoss(h.congestionWindow)
	}
	if h.congestionWindow < h.minCongestionWindow {
		h.congestionWindow = h.minCongestionWindow
	}
	h.slowstartThreshold = h.congestionWindow
	h.largestSentAtLastCutback = h.largestSentPacketNumber
	// reset packet count from congestion avoidance mode. We start
	// counting again when we're out of recovery.
	h.numAckedPackets = 0
	//log.Printf("congestion = %d", h.congestionWindow)
}

func (h *cubicSender) RenoBeta() float32 {
	// kNConnectionBeta is the backoff factor after loss for our N-connection
	// emulation, which emulates the effective backoff of an ensemble of N
	// TCP-Reno connections on a single loss event. The effective multiplier is
	// computed as:
	return (float32(h.numConnections) - 1. + renoBeta) / float32(h.numConnections)
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
		log.Printf("LIMITED: %d, thresh: %d\n", c.congestionWindow, c.slowstartThreshold)
		return
	}
	if c.congestionWindow >= c.maxCongestionWindow {
		return
	}
	if c.InSlowStart() {
		// TCP slow start, exponential growth, increase by one for each ACK.
		c.congestionWindow += protocol.ByteCount(uint32(protocol.DefaultTCPMSS) * c.p)
		log.Printf("AFTER SS: %d, thresh: %d\n", c.congestionWindow, c.slowstartThreshold)
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
		c.congestionWindow = utils.MinByteCount(c.maxCongestionWindow, c.cubic.CongestionWindowAfterAck(ackedBytes, c.congestionWindow, c.rttStats.MinRTT(), eventTime))
		log.Printf("AFTER CA: %d, thresh: %d\n", c.congestionWindow, c.slowstartThreshold)
	}
}

func (h *cubicSender) isCwndLimited(bytesInFlight protocol.ByteCount) bool {
	congestionWindow := h.GetCongestionWindow()
	if bytesInFlight >= congestionWindow {
		return true
	}
	availableBytes := congestionWindow - bytesInFlight
	slowStartLimited := h.InSlowStart() && bytesInFlight > congestionWindow/2
	return slowStartLimited || availableBytes <= protocol.ByteCount(uint32(maxBurstBytes)*h.p)
}

// BandwidthEstimate returns the current bandwidth estimate
func (h *cubicSender) BandwidthEstimate() Bandwidth {
	srtt := h.rttStats.SmoothedRTT()
	if srtt == 0 {
		// If we haven't measured an rtt, the bandwidth estimate is unknown.
		return 0
	}
	return BandwidthFromDelta(h.GetCongestionWindow(), srtt)
}

// HybridSlowStart returns the hybrid slow start instance for testing
func (h *cubicSender) HybridSlowStart() *HybridSlowStart {
	return &h.hybridSlowStart
}

// SetNumEmulatedConnections sets the number of emulated connections
func (h *cubicSender) SetNumEmulatedConnections(n int) {
	h.numConnections = utils.Max(n, 1)
	h.cubic.SetNumConnections(h.numConnections)
}

// OnRetransmissionTimeout is called on an retransmission timeout
func (h *cubicSender) OnRetransmissionTimeout(packetsRetransmitted bool) {
	h.largestSentAtLastCutback = protocol.InvalidPacketNumber
	if !packetsRetransmitted {
		return
	}
	h.hybridSlowStart.Restart()
	h.cubic.Reset()
	h.slowstartThreshold = h.congestionWindow / 2
	h.congestionWindow = h.minCongestionWindow
}

// OnConnectionMigration is called when the connection is migrated (?)
func (h *cubicSender) OnConnectionMigration() {
	h.hybridSlowStart.Restart()
	h.prr = PrrSender{}
	h.largestSentPacketNumber = protocol.InvalidPacketNumber
	h.largestAckedPacketNumber = protocol.InvalidPacketNumber
	h.largestSentAtLastCutback = protocol.InvalidPacketNumber
	h.lastCutbackExitedSlowstart = false
	h.cubic.Reset()
	h.numAckedPackets = 0
	h.congestionWindow = h.initialCongestionWindow
	h.slowstartThreshold = h.initialMaxCongestionWindow
	h.maxCongestionWindow = h.initialMaxCongestionWindow
}

// SetSlowStartLargeReduction allows enabling the SSLR experiment
func (h *cubicSender) SetSlowStartLargeReduction(enabled bool) {
	h.slowStartLargeReduction = enabled
}
*/