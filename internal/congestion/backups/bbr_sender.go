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
	defaultMinimumCongestionWindow protocol.ByteCount = 2 * protocol.DefaultTCPMSS
	rtt0                           uint32             = 25
)

type BBRSender struct {
	hybridSlowStart HybridSlowStart
	prr             PrrSender
	rttStats        *RTTStats
	stats           connectionStats
	bbr             *BBR
	p               uint32
	firstRun        uint32
	ssCounter       uint32

	noPRR bool

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
	log.Println("Congestion window size recorded in the file successfully.")
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
	log.Println("Congestion window size recorded in the file successfully.")
}

// NewbbrSender makes a new bbr sender
func NewBBRSender(clock Clock, rttStats *RTTStats, initialCongestionWindow, initialMaxCongestionWindow protocol.ByteCount) *BBRSender {
	return &BBRSender{
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
		bbr:                        NewBBR(clock),
		ssCounter:                  0,
		firstRun:                   1,
		p:                          1,
	}
}

// TimeUntilSend returns when the next packet should be sent.
func (b *BBRSender) TimeUntilSend(bytesInFlight protocol.ByteCount) time.Duration {
	if !b.noPRR && b.InRecovery() {
		// PRR is used when in recovery.
		if b.prr.CanSend(b.GetCongestionWindow(), bytesInFlight, b.GetSlowStartThreshold()) {
			return 0
		}
	}
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
	if b.InRecovery() {
		// PRR is used when in recovery.
		b.prr.OnPacketSent(bytes)
	}
	b.largestSentPacketNumber = packetNumber
	b.hybridSlowStart.OnPacketSent(packetNumber)
}

func (b *BBRSender) CanSend(bytesInFlight protocol.ByteCount) bool {
	if !b.noPRR && b.InRecovery() {
		return b.prr.CanSend(b.GetCongestionWindow(), bytesInFlight, b.GetSlowStartThreshold())
	}
	return bytesInFlight < b.GetCongestionWindow()
}

func (b *BBRSender) InRecovery() bool {
	return b.largestAckedPacketNumber != protocol.InvalidPacketNumber && b.largestAckedPacketNumber <= b.largestSentAtLastCutback
}

func (b *BBRSender) InSlowStart() bool {
	return b.GetCongestionWindow() < b.GetSlowStartThreshold()
}

func (b *BBRSender) GetCongestionWindow() protocol.ByteCount {
	return b.congestionWindow
}

func (b *BBRSender) GetSlowStartThreshold() protocol.ByteCount {
	return b.slowstartThreshold
}

func (b *BBRSender) ExitSlowstart() {
	b.slowstartThreshold = b.congestionWindow
}

func (b *BBRSender) SlowstartThreshold() protocol.ByteCount {
	return b.slowstartThreshold
}

func (b *BBRSender) MaybeExitSlowStart() {
	if b.InSlowStart() && (b.congestionWindow >= b.slowstartThreshold) {
		b.ExitSlowstart()
	}
}

func (b *BBRSender) OnPacketAcked(
	ackedPacketNumber protocol.PacketNumber,
	ackedBytes protocol.ByteCount,
	priorInFlight protocol.ByteCount,
	eventTime time.Time,
) {
	b.largestAckedPacketNumber = utils.MaxPacketNumber(ackedPacketNumber, b.largestAckedPacketNumber)
	if b.InRecovery() {
		// PRR is used when in recovery.
		if !b.noPRR {
			b.prr.OnPacketAcked(ackedBytes)
		}
		return
	}
	b.maybeIncreaseCwnd(ackedPacketNumber, ackedBytes, priorInFlight, eventTime)
	if b.InSlowStart() {
		b.hybridSlowStart.OnPacketAcked(ackedPacketNumber)
	}
}

func (b *BBRSender) OnPacketLost(
	packetNumber protocol.PacketNumber,
	lostBytes protocol.ByteCount,
	priorInFlight protocol.ByteCount,
) {
	b.RecordPacketLoss()
	log.Printf("LOST PACKET, congestionWindow: %d, ssthresh: %d\n", b.congestionWindow, b.slowstartThreshold)
	// TCP NewReno (RFC6582) says that once a loss occurs, any losses in packets
	// already sent should be treated as a single loss event, since it's expected.
	if packetNumber <= b.largestSentAtLastCutback {
		log.Printf("WHAT THE HELLL")
		if b.lastCutbackExitedSlowstart {
			b.stats.slowstartPacketsLost++
			b.stats.slowstartBytesLost += lostBytes
			if b.slowStartLargeReduction {
				// Reduce congestion window by lost_bytes for every loss.
				b.congestionWindow = utils.MaxByteCount(b.congestionWindow-lostBytes, b.minSlowStartExitWindow)
				b.RecordCongestionWindow()
				b.slowstartThreshold = b.congestionWindow
			}
		}
		return
	}
	b.lastCutbackExitedSlowstart = b.InSlowStart()
	if b.InSlowStart() {
		b.stats.slowstartPacketsLost++
	}

	if !b.noPRR {
		b.prr.OnPacketLost(priorInFlight)
	}
	// TODO(chromium): Separate out all of slow start into a separate class.
	if b.slowStartLargeReduction && b.InSlowStart() {
		if b.congestionWindow >= 2*b.initialCongestionWindow {
			b.minSlowStartExitWindow = b.congestionWindow / 2
		}
		b.congestionWindow -= protocol.DefaultTCPMSS
	} else {
		b.congestionWindow = b.bbr.CongestionWindowAfterPacketLoss(b.congestionWindow)
	}
	if b.congestionWindow < b.minCongestionWindow {
		b.congestionWindow = b.minCongestionWindow
	}
	b.RecordCongestionWindow()
	b.slowstartThreshold = b.congestionWindow
	b.largestSentAtLastCutback = b.largestSentPacketNumber
	// reset packet count from congestion avoidance mode. We start
	// counting again when we're out of recovery.
	b.numAckedPackets = 0
	//log.Printf("congestion = %d", b.congestionWindow)
}

// Called when we receive an ack. Normal TCP tracks how many packets one ack
// represents, but quic has a separate ack for each packet.
func (b *BBRSender) maybeIncreaseCwnd(
	_ protocol.PacketNumber,
	ackedBytes protocol.ByteCount,
	priorInFlight protocol.ByteCount,
	eventTime time.Time,
) {
	// Do not increase the congestion window unless the sender is close to using
	// the current window.
	if !b.isCwndLimited(priorInFlight) {
		log.Printf("LIMITED: %d, thresh %d, infli: %d, bandwidth: %d\n", b.congestionWindow, b.slowstartThreshold, priorInFlight, b.BandwidthEstimate())
		//log.Printf("pnum: %d, ssthresh: %d, b.congestionWindow: %d, ssCounter: %d, inFlight = %d\n", b.p, b.slowstartThreshold, b.congestionWindow, b.ssCounter, priorInFlight)
		b.bbr.OnApplicationLimited()
		return
	}
	if b.congestionWindow >= b.maxCongestionWindow {
		return
	}
	if b.InSlowStart() {
		//log.Printf("pnum: %d, ssthresh: %d, b.congestionWindow: %d, ssCounter: %d, inFlight = %d\n", b.p, b.slowstartThreshold, b.congestionWindow, b.ssCounter, priorInFlight)
		// TCP slow start, exponential growth, increase by one for each ACK.
		b.congestionWindow += protocol.ByteCount(uint32(protocol.DefaultTCPMSS) * (uint32(math.Pow(2, float64(b.p))) - 1))
		log.Printf("AFTER SS: %d, thresh %d, infli: %d, bandwidth: %d\n", b.congestionWindow, b.slowstartThreshold, priorInFlight, b.BandwidthEstimate())
		b.RecordCongestionWindow()
		return
	} else {
		//log.Println("ELSE\n")
		//log.Printf("pnum: %d, ssthresh: %d, b.congestionWindow: %d, ssCounter: %d, inFlight = %d\n", b.p, h.slowstartThreshold, h.congestionWindow, h.ssCounter, priorInFlight)
		b.congestionWindow = utils.MinByteCount(b.maxCongestionWindow, protocol.ByteCount(uint32(b.bbr.CongestionWindowAfterAck(ackedBytes, b.congestionWindow, b.rttStats.MinRTT(), eventTime, b.p))))
		log.Printf("AFTER CA: %d, thresh %d, infli: %d, bandwidth: %d\n", b.congestionWindow, b.slowstartThreshold, priorInFlight, b.BandwidthEstimate())
		b.RecordCongestionWindow()
	}
}

func (b *BBRSender) isCwndLimited(bytesInFlight protocol.ByteCount) bool {
	congestionWindow := b.GetCongestionWindow()
	if bytesInFlight >= congestionWindow {
		return true
	}
	availableBytes := congestionWindow - bytesInFlight

	slowStartLimited := b.InSlowStart() && bytesInFlight > congestionWindow/2
	return slowStartLimited || availableBytes <= protocol.ByteCount(uint32(maxBurstBytes)*b.p)
}

// BandwidthEstimate returns the current bandwidth estimate
func (b *BBRSender) BandwidthEstimate() Bandwidth {
	srtt := b.rttStats.SmoothedRTT()
	if srtt == 0 {
		// If we haven't measured an rtt, the bandwidth estimate is unknown.
		return 0
	}
	return BandwidthFromDelta(b.GetCongestionWindow(), srtt)
}

// HybridSlowStart returns the hybrid slow start instance for testing
func (b *BBRSender) HybridSlowStart() *HybridSlowStart {
	return &b.hybridSlowStart
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
	b.hybridSlowStart.Restart()
	b.bbr.Reset()
	b.slowstartThreshold = b.congestionWindow / 2
	b.congestionWindow = b.minCongestionWindow
}

// OnConnectionMigration is called when the connection is migrated (?)
func (b *BBRSender) OnConnectionMigration() {
	b.hybridSlowStart.Restart()
	b.prr = PrrSender{}
	b.largestSentPacketNumber = protocol.InvalidPacketNumber
	b.largestAckedPacketNumber = protocol.InvalidPacketNumber
	b.largestSentAtLastCutback = protocol.InvalidPacketNumber
	b.lastCutbackExitedSlowstart = false
	b.bbr.Reset()
	b.numAckedPackets = 0
	b.congestionWindow = b.initialCongestionWindow
	b.slowstartThreshold = b.initialMaxCongestionWindow
	b.maxCongestionWindow = b.initialMaxCongestionWindow
}

// SetSlowStartLargeReduction allows enabling the SSLR experiment
func (b *BBRSender) SetSlowStartLargeReduction(enabled bool) {
	b.slowStartLargeReduction = enabled
}
