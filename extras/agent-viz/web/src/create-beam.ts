import type { AgentRing } from './agents';
import type { AgentInfo } from './types';

const BEAM_CHARGE_DURATION = 400; // ms for beam to charge up (slightly longer than destroy)
const BEAM_TRAVEL_DURATION = 500; // ms for beam to travel
const BEAM_LINGER_DURATION = 400; // ms for impact glow to fade
const TOTAL_BEAM_DURATION = BEAM_CHARGE_DURATION + BEAM_TRAVEL_DURATION + BEAM_LINGER_DURATION;

interface ActiveCreateBeam {
  fromAgent: string;
  newAgentInfo: AgentInfo;
  startTime: number;
  fromColor: string;
  // Captured positions at beam start (frozen ring)
  frozenFrom: { x: number; y: number };
  targetPos: { x: number; y: number };
  agentAdded: boolean; // whether we've added the agent to the ring
}

export class CreateBeamRenderer {
  private beams: ActiveCreateBeam[] = [];
  private agentRing: AgentRing | null = null;

  setAgentRing(ring: AgentRing): void {
    this.agentRing = ring;
  }

  addBeam(
    requestedByName: string,
    newAgentInfo: AgentInfo,
    agentRing: AgentRing
  ): void {
    const fromPos = agentRing.getAgentPosition(requestedByName);
    if (!fromPos) return;

    // Freeze ring so positions stay fixed during beam travel
    agentRing.freezeRebalance();

    // Claim a slot position (accounts for other pending beams, adds jitter)
    const targetPos = agentRing.claimNextSlotPosition();

    this.beams.push({
      fromAgent: requestedByName,
      newAgentInfo,
      startTime: Date.now(),
      fromColor: agentRing.getAgentColor(requestedByName),
      frozenFrom: { ...fromPos },
      targetPos: { ...targetPos },
      agentAdded: false,
    });
  }

  reset(): void {
    this.beams = [];
  }

  draw(ctx: CanvasRenderingContext2D): void {
    const now = Date.now();

    this.beams = this.beams.filter((b) => {
      const expired = now - b.startTime >= TOTAL_BEAM_DURATION;
      if (expired && this.agentRing) {
        this.agentRing.unfreezeRebalance();
      }
      return !expired;
    });

    for (const beam of this.beams) {
      const elapsed = now - beam.startTime;
      const { frozenFrom: from, targetPos: to } = beam;

      // Add agent to ring when beam arrives at target
      if (
        !beam.agentAdded &&
        elapsed >= BEAM_CHARGE_DURATION + BEAM_TRAVEL_DURATION &&
        this.agentRing
      ) {
        beam.agentAdded = true;
        this.agentRing.addAgent(beam.newAgentInfo);
      }

      if (elapsed < BEAM_CHARGE_DURATION) {
        // Charge-up phase: pulsing green glow at source
        const t = elapsed / BEAM_CHARGE_DURATION;
        const pulseAlpha = 0.3 + 0.7 * Math.sin(t * Math.PI * 3);

        ctx.save();
        ctx.globalAlpha = pulseAlpha * t;
        ctx.shadowBlur = 20;
        ctx.shadowColor = '#88ff44';
        ctx.beginPath();
        ctx.arc(from.x, from.y, 8 + 6 * t, 0, Math.PI * 2);
        ctx.fillStyle = '#aaff66';
        ctx.fill();
        ctx.restore();
      } else if (elapsed < BEAM_CHARGE_DURATION + BEAM_TRAVEL_DURATION) {
        // Beam travel phase: yellow-green slime beam
        const travelElapsed = elapsed - BEAM_CHARGE_DURATION;
        const t = travelElapsed / BEAM_TRAVEL_DURATION;
        const eased = 1 - Math.pow(1 - t, 2); // ease-out quad

        const beamEndX = from.x + (to.x - from.x) * eased;
        const beamEndY = from.y + (to.y - from.y) * eased;

        ctx.save();
        ctx.shadowBlur = 15;
        ctx.shadowColor = '#88ff44';

        // Outer glow (wide, soft green)
        ctx.beginPath();
        ctx.moveTo(from.x, from.y);
        ctx.lineTo(beamEndX, beamEndY);
        ctx.strokeStyle = 'rgba(100, 255, 50, 0.2)';
        ctx.lineWidth = 12;
        ctx.stroke();

        // Mid glow
        ctx.beginPath();
        ctx.moveTo(from.x, from.y);
        ctx.lineTo(beamEndX, beamEndY);
        ctx.strokeStyle = 'rgba(150, 255, 80, 0.4)';
        ctx.lineWidth = 6;
        ctx.stroke();

        // Core beam (bright yellow-green)
        ctx.beginPath();
        ctx.moveTo(from.x, from.y);
        ctx.lineTo(beamEndX, beamEndY);
        ctx.strokeStyle = 'rgba(200, 255, 150, 0.8)';
        ctx.lineWidth = 2.5;
        ctx.stroke();

        // Beam tip glow — bright droplet
        ctx.beginPath();
        ctx.arc(beamEndX, beamEndY, 8, 0, Math.PI * 2);
        ctx.fillStyle = 'rgba(180, 255, 100, 0.7)';
        ctx.fill();
        ctx.beginPath();
        ctx.arc(beamEndX, beamEndY, 4, 0, Math.PI * 2);
        ctx.fillStyle = 'rgba(240, 255, 200, 0.9)';
        ctx.fill();

        // Slime drip particles along the beam (organic feel)
        const dripCount = Math.floor(t * 6);
        for (let i = 0; i < dripCount; i++) {
          const ct = Math.random() * eased;
          const cx = from.x + (to.x - from.x) * ct;
          const cy = from.y + (to.y - from.y) * ct;
          // Drips fall slightly downward
          const dripOffset = Math.random() * 8 + 2;
          ctx.beginPath();
          ctx.arc(cx + (Math.random() - 0.5) * 6, cy + dripOffset, 1.5 + Math.random(), 0, Math.PI * 2);
          ctx.fillStyle = `rgba(140, 255, 80, ${0.3 + Math.random() * 0.4})`;
          ctx.fill();
        }

        ctx.restore();
      } else {
        // Linger phase: impact bloom where agent spawns
        const lingerElapsed = elapsed - BEAM_CHARGE_DURATION - BEAM_TRAVEL_DURATION;
        const t = lingerElapsed / BEAM_LINGER_DURATION;
        const alpha = 1 - t;

        ctx.save();
        ctx.globalAlpha = alpha;

        // Fading beam trail
        ctx.shadowBlur = 8 * alpha;
        ctx.shadowColor = '#88ff44';
        ctx.beginPath();
        ctx.moveTo(from.x, from.y);
        ctx.lineTo(to.x, to.y);
        ctx.strokeStyle = `rgba(100, 255, 50, ${0.3 * alpha})`;
        ctx.lineWidth = 2 * alpha;
        ctx.stroke();

        // Expanding birth ring at target
        const ringR = 10 + 25 * t;
        ctx.beginPath();
        ctx.arc(to.x, to.y, ringR, 0, Math.PI * 2);
        ctx.strokeStyle = `rgba(150, 255, 80, ${0.7 * alpha})`;
        ctx.lineWidth = 2.5 * (1 - t);
        ctx.stroke();

        // Inner spawn flash
        if (t < 0.4) {
          const flashAlpha = (1 - t / 0.4) * alpha;
          ctx.beginPath();
          ctx.arc(to.x, to.y, 12 * (1 - t * 0.5), 0, Math.PI * 2);
          ctx.fillStyle = `rgba(200, 255, 150, ${flashAlpha * 0.6})`;
          ctx.shadowBlur = 20;
          ctx.shadowColor = '#ccff88';
          ctx.fill();
        }

        ctx.restore();
      }
    }
  }
}
