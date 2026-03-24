import ForceGraph from 'force-graph';
import type { FileNode, GraphNode, GraphLink } from './types';

type ForceGraphInstance = InstanceType<typeof ForceGraph>;

const ROOT_RADIUS = 10;
const DIR_RADIUS = 6;
const FILE_RADIUS = 3;
const HIGHLIGHT_DURATION = 3000; // ms
const REVEAL_DURATION = 600; // ms for file materialize animation
const REVEAL_LABEL_DURATION = 3000; // ms to keep label prominently visible after reveal

export class FileGraph {
  private graph: ForceGraphInstance;
  private nodes: Map<string, GraphNode> = new Map();
  private container: HTMLElement;

  constructor(container: HTMLElement) {
    this.container = container;
    this.graph = new ForceGraph(container)
      .nodeId('id')
      .nodeLabel('name')
      .nodeCanvasObject((node, ctx, globalScale) => this.drawNode(node as GraphNode, ctx, globalScale))
      .nodePointerAreaPaint((node, color, ctx) => this.drawNodeArea(node as GraphNode, color, ctx))
      .linkVisibility((link) => {
        const src = typeof link.source === 'object' ? link.source as GraphNode : this.nodes.get(link.source as string);
        const tgt = typeof link.target === 'object' ? link.target as GraphNode : this.nodes.get(link.target as string);
        return (src?.visible ?? false) && (tgt?.visible ?? false);
      })
      .linkColor(() => 'rgba(255,255,255,0.15)')
      .linkWidth(1)
      .d3AlphaDecay(0.02)
      .d3VelocityDecay(0.3)
      .cooldownTicks(200)
      .warmupTicks(100)
      .backgroundColor('transparent');
  }

  getGraph(): InstanceType<typeof ForceGraph> {
    return this.graph;
  }

  init(files: FileNode[]): void {
    const graphNodes: GraphNode[] = [];
    const links: GraphLink[] = [];

    for (const f of files) {
      const node: GraphNode = {
        id: f.id,
        name: f.name,
        isDir: f.isDir,
        parent: f.parent,
        highlighted: false,
        visible: true,
      };
      this.nodes.set(f.id, node);
      graphNodes.push(node);
    }

    // Create links from parent-child relationships
    for (const f of files) {
      if (f.id === '.') continue; // root has no parent link
      const parent = f.parent || '.';
      if (this.nodes.has(parent)) {
        links.push({ source: parent, target: f.id });
      }
    }

    this.graph.graphData({ nodes: graphNodes, links });

    // Center on graph after layout settles
    setTimeout(() => {
      this.graph.zoomToFit(400, 80);
    }, 2000);
  }

  addFile(filePath: string, visible = false): void {
    if (this.nodes.has(filePath)) return;

    // Ensure root node exists
    this.ensureRoot();

    const name = filePath.includes('/') ? filePath.substring(filePath.lastIndexOf('/') + 1) : filePath;
    const parent = filePath.includes('/') ? filePath.substring(0, filePath.lastIndexOf('/')) : '.';

    // Ensure parent directories exist
    if (parent !== '.' && !this.nodes.has(parent)) {
      this.addFile(parent + '/', true); // directories are visible immediately
    }

    const isDir = filePath.endsWith('/');
    const id = isDir ? filePath.slice(0, -1) : filePath;
    if (this.nodes.has(id)) return;

    const actualName = isDir ? (id.includes('/') ? id.substring(id.lastIndexOf('/') + 1) : id) : name;
    const actualParent = isDir
      ? (id.includes('/') ? id.substring(0, id.lastIndexOf('/')) : '.')
      : parent;

    const node: GraphNode = {
      id,
      name: actualName,
      isDir,
      parent: actualParent,
      highlighted: false,
      visible: visible || isDir, // directories always visible immediately
    };
    this.nodes.set(id, node);

    // Add to force graph
    const { nodes: existingNodes, links: existingLinks } = this.graph.graphData();
    const newNodes = [...existingNodes, node];
    const newLinks = [...existingLinks];

    const linkParent = actualParent || '.';
    if (this.nodes.has(linkParent)) {
      newLinks.push({ source: linkParent, target: id });
    }

    this.graph.graphData({ nodes: newNodes, links: newLinks });
  }

  private ensureRoot(): void {
    if (this.nodes.has('.')) return;
    const root: GraphNode = {
      id: '.',
      name: '/workspace',
      isDir: true,
      parent: '',
      highlighted: false,
      visible: true,
    };
    this.nodes.set('.', root);
    const { nodes, links } = this.graph.graphData();
    this.graph.graphData({ nodes: [...nodes, root], links });
  }

  hasFile(filePath: string): boolean {
    return this.nodes.has(filePath);
  }

  revealFile(filePath: string): void {
    const node = this.nodes.get(filePath);
    if (node && !node.visible) {
      node.visible = true;
      node.revealTime = Date.now();
    }
  }

  highlightFile(filePath: string): void {
    const node = this.nodes.get(filePath);
    if (node) {
      node.highlighted = true;
      node.highlightTime = Date.now();
    }
    // Also highlight parent directories
    let parent = this.getParentPath(filePath);
    while (parent && parent !== '.') {
      const pNode = this.nodes.get(parent);
      if (pNode) {
        pNode.highlighted = true;
        pNode.highlightTime = Date.now();
      }
      parent = this.getParentPath(parent);
    }
  }

  getNodePosition(nodeId: string): { x: number; y: number } | null {
    const node = this.nodes.get(nodeId);
    if (node && node.x !== undefined && node.y !== undefined) {
      return { x: node.x, y: node.y };
    }
    return null;
  }

  reset(): void {
    this.nodes.clear();
    this.graph.graphData({ nodes: [], links: [] });
  }

  resize(width: number, height: number): void {
    this.graph.width(width).height(height);
  }

  private drawNode(node: GraphNode, ctx: CanvasRenderingContext2D, globalScale: number): void {
    // Skip invisible nodes
    if (!node.visible) return;

    const isRoot = node.id === '.';
    const r = isRoot ? ROOT_RADIUS : node.isDir ? DIR_RADIUS : FILE_RADIUS;
    const x = node.x ?? 0;
    const y = node.y ?? 0;

    // Reveal animation (scale-in when file materializes)
    let revealScale = 1;
    if (node.revealTime) {
      const elapsed = Date.now() - node.revealTime;
      if (elapsed < REVEAL_DURATION) {
        const t = elapsed / REVEAL_DURATION;
        // Elastic ease for a "pop" appearance
        revealScale = elasticOut(t);
      } else if (elapsed > REVEAL_LABEL_DURATION) {
        // Clear revealTime after label prominence period ends
        node.revealTime = undefined;
      }
    }

    // Check highlight fade
    let alpha = 1;
    let glowing = false;
    if (node.highlighted && node.highlightTime) {
      const elapsed = Date.now() - node.highlightTime;
      if (elapsed > HIGHLIGHT_DURATION) {
        node.highlighted = false;
      } else {
        glowing = true;
        alpha = 1 - elapsed / HIGHLIGHT_DURATION;
      }
    }

    const scaledR = r * revealScale;
    if (scaledR < 0.1) return;

    ctx.save();
    if (revealScale < 1) {
      ctx.globalAlpha = Math.min(1, revealScale * 2);
    }

    // Glow effect
    if (glowing) {
      ctx.save();
      ctx.globalAlpha = alpha * 0.6;
      ctx.shadowBlur = 15;
      ctx.shadowColor = '#4fc3f7';
      ctx.beginPath();
      ctx.arc(x, y, scaledR + 4, 0, Math.PI * 2);
      ctx.fillStyle = '#4fc3f7';
      ctx.fill();
      ctx.restore();
    }

    // Node circle
    ctx.beginPath();
    ctx.arc(x, y, scaledR, 0, Math.PI * 2);
    if (isRoot) {
      ctx.fillStyle = glowing ? '#fff176' : '#ffa726';
    } else if (node.isDir) {
      ctx.fillStyle = glowing ? '#4fc3f7' : '#546e7a';
    } else {
      ctx.fillStyle = glowing ? '#81d4fa' : '#78909c';
    }
    ctx.fill();

    // Border
    ctx.strokeStyle = 'rgba(255,255,255,0.3)';
    ctx.lineWidth = 0.5;
    ctx.stroke();

    // Label — always visible for root and recently revealed files, otherwise when zoomed in
    const recentlyRevealed = node.revealTime !== undefined;
    if ((isRoot || recentlyRevealed || globalScale > 0.8) && revealScale > 0.5) {
      const fontSize = isRoot ? Math.max(5, 12 / globalScale) : Math.max(3, 10 / globalScale);
      ctx.font = `${isRoot ? 'bold ' : ''}${fontSize}px sans-serif`;
      ctx.textAlign = 'center';
      ctx.textBaseline = 'top';
      const labelAlpha = isRoot ? 0.9 : recentlyRevealed ? 0.9 : 0.7;
      ctx.fillStyle = `rgba(255,255,255,${labelAlpha})`;
      ctx.fillText(node.name, x, y + scaledR + 2);
    }

    ctx.restore();
  }

  private drawNodeArea(node: GraphNode, color: string, ctx: CanvasRenderingContext2D): void {
    if (!node.visible) return;
    const r = node.isDir ? DIR_RADIUS : FILE_RADIUS;
    ctx.beginPath();
    ctx.arc(node.x ?? 0, node.y ?? 0, r + 2, 0, Math.PI * 2);
    ctx.fillStyle = color;
    ctx.fill();
  }

  private getParentPath(path: string): string {
    const idx = path.lastIndexOf('/');
    if (idx <= 0) return '.';
    return path.substring(0, idx);
  }
}

function elasticOut(t: number): number {
  if (t === 0 || t === 1) return t;
  const p = 0.4;
  return Math.pow(2, -10 * t) * Math.sin(((t - p / 4) * (2 * Math.PI)) / p) + 1;
}
