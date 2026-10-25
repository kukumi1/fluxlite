import {
  ACESFilmicToneMapping,
  BufferAttribute,
  CanvasTexture,
  CapsuleGeometry,
  Color,
  ConeGeometry,
  DirectionalLight,
  ExtrudeGeometry,
  Group,
  HemisphereLight,
  LatheGeometry,
  Mesh,
  MeshBasicMaterial,
  MathUtils,
  MeshPhysicalMaterial,
  PCFShadowMap,
  PerspectiveCamera,
  PlaneGeometry,
  PMREMGenerator,
  Scene,
  ShadowMaterial,
  Shape,
  SphereGeometry,
  SplineCurve,
  TorusGeometry,
  TubeGeometry,
  Vector2,
  Vector3,
  CatmullRomCurve3,
  WebGLRenderer,
  type BufferGeometry,
  type Material,
  type Object3D,
} from "three";
import { RoomEnvironment } from "three/addons/environments/RoomEnvironment.js";
import { growFur, setFurScale, type FurOptions } from "./cat3d-fur";

export type CatMood = "idle" | "watch" | "shy" | "peek";

export interface GazePoint {
  x: number;
  y: number;
}

export interface CatSceneOptions {
  /** Pixels between the cat and the target at which it turns its head fully. */
  reach: number;
  /** Overrides the pointer as the thing to look at while it returns a point. */
  focus?: () => GazePoint | null;
  /** Sit the cat on a cushion beside a ball of yarn (the sign-in page). */
  scenery?: boolean;
  /** Called once, after the first frame has been drawn. */
  onReady?: () => void;
}

// A white kitten with grey tabby markings, a lavender collar and a heart tag.
const WHITE = "#fbf8f5";
const GREY = "#9b948f";
const TABBY = "#5f5853";
const INNER_EAR = "#f1a6a8";
const IRIS = "#8ea596";
const COLLAR = "#b7a2ea";

const KEY_LIGHT = new Vector3(3.5, 7, 6);

const CUSHION = "#cdbdf2";
const YARN_RADIUS = 0.3;
const YARN_HOME = new Vector3(-1.2, 0.62, 0.95);
/** How far the cat sinks into the cushion's top. */
const CUSHION_SEAT = 0.34;

// One swat at the yarn, in seconds: reach out, strike, and settle back.
const TAP_DURATION = 1.1;
const TAP_STRIKE = 0.45;
const ROLL_OUT = 0.6;
const ROLL_BACK_START = 1.6;
const ROLL_BACK_END = 3.6;
const ROLL_DISTANCE = 0.3;

// Shoulder to the middle of the paw, in scene units.
const LEG_LENGTH = 1.15;

// Leg angles about the shoulder (radians around x): resting on the ground in
// front of the chest, raised over the eyes, and halfway for a one-eyed peek.
const ARM_REST = -0.3;
const ARM_COVER = -2.44;
const ARM_PEEK = -2.0;

const MAX_YAW = 0.75;
const MIN_PITCH = -0.35;
const MAX_PITCH = 0.45;

function damp(current: number, target: number, rate: number, dt: number): number {
  return current + (target - current) * (1 - Math.exp(-rate * dt));
}

function clamp(value: number, min: number, max: number): number {
  return Math.min(max, Math.max(min, value));
}

function smoothstep(value: number, edge0: number, edge1: number): number {
  return MathUtils.smoothstep(value, edge0, edge1);
}

/** Bakes per-vertex colour, so markings follow the surface without extra meshes or seams. */
function paint(geometry: BufferGeometry, colorAt: (position: Vector3, uv: Vector2) => Color) {
  const positions = geometry.getAttribute("position");
  const uvs = geometry.getAttribute("uv");
  const colors = new Float32Array(positions.count * 3);
  const position = new Vector3();
  const uv = new Vector2();
  for (let i = 0; i < positions.count; i++) {
    position.set(positions.getX(i), positions.getY(i), positions.getZ(i));
    if (uvs) uv.set(uvs.getX(i), uvs.getY(i));
    colorAt(position, uv).toArray(colors, i * 3);
  }
  geometry.setAttribute("color", new BufferAttribute(colors, 3));
}

function contactShadowTexture(): CanvasTexture {
  const canvas = document.createElement("canvas");
  canvas.width = canvas.height = 128;
  const ctx = canvas.getContext("2d");
  if (ctx) {
    const gradient = ctx.createRadialGradient(64, 64, 0, 64, 64, 64);
    gradient.addColorStop(0, "rgba(40,34,48,0.42)");
    gradient.addColorStop(0.55, "rgba(40,34,48,0.13)");
    gradient.addColorStop(1, "rgba(40,34,48,0)");
    ctx.fillStyle = gradient;
    ctx.fillRect(0, 0, 128, 128);
  }
  return new CanvasTexture(canvas);
}

/**
 * A fluffy kitten drawn with three.js: shell fur gives it a soft outline, an
 * environment map gives it believable light, and every part that moves (head,
 * arms, tail, eyes) is its own pivot so poses blend smoothly.
 */
export class CatScene {
  private readonly renderer: WebGLRenderer;
  private readonly scene = new Scene();
  private readonly camera = new PerspectiveCamera(28, 1, 0.1, 50);
  private readonly root = new Group();
  private readonly body = new Group();
  private readonly headPivot = new Group();
  private readonly ears: Group[] = [];
  private readonly eyes: Object3D[] = [];
  private readonly arms: Group[] = [];
  private readonly tail = new Group();
  private readonly scenery = new Group();
  private readonly yarn = new Group();
  private readonly disposables: (BufferGeometry | Material | CanvasTexture)[] = [];

  private readonly host: HTMLElement;
  private readonly options: CatSceneOptions;
  private readonly reducedMotion = window.matchMedia("(prefers-reduced-motion: reduce)").matches;
  private pointer: GazePoint | null = null;
  private mood: CatMood = "idle";
  private yaw = 0;
  private pitch = 0;
  private armAngles = [ARM_REST, ARM_REST];
  private elapsed = 0;
  private nextBlink = 2.5;
  private blinkStart = -1;
  private nextTwitch = 4;
  private twitchStart = -1;
  private shakeStart = -1;
  private hopStart = -1;
  private nextTap = 6;
  private tapStart = -1;
  private seat = 0;
  private frame = 0;
  private lastTime = 0;
  private visible = true;
  private drawn = false;
  private readonly resizeObserver: ResizeObserver;
  private readonly intersectionObserver: IntersectionObserver;

  constructor(host: HTMLElement, options: CatSceneOptions) {
    this.host = host;
    this.options = options;
    this.renderer = new WebGLRenderer({ antialias: true, alpha: true, powerPreference: "low-power" });
    this.renderer.setPixelRatio(Math.min(window.devicePixelRatio, 2));
    this.renderer.toneMapping = ACESFilmicToneMapping;
    this.renderer.toneMappingExposure = 0.92;
    this.renderer.shadowMap.enabled = true;
    this.renderer.shadowMap.type = PCFShadowMap;
    this.renderer.domElement.setAttribute("aria-hidden", "true");
    host.appendChild(this.renderer.domElement);

    const pmrem = new PMREMGenerator(this.renderer);
    const environment = new RoomEnvironment();
    this.scene.environment = pmrem.fromScene(environment, 0.04).texture;
    this.scene.environmentIntensity = 0.45;
    environment.dispose();
    pmrem.dispose();

    if (options.scenery) {
      this.camera.position.set(0, 2.5, 12);
      this.camera.lookAt(0, 1.9, 0);
    } else {
      this.camera.position.set(0, 2.1, 10.5);
      this.camera.lookAt(0, 1.72, 0);
    }

    this.addLights();
    this.buildCat();
    this.scene.add(this.root);
    if (options.scenery) {
      this.seat = CUSHION_SEAT;
      this.buildScenery();
      this.scene.add(this.scenery);
    }

    this.resizeObserver = new ResizeObserver(() => this.resize());
    this.resizeObserver.observe(host);
    this.resize();

    this.intersectionObserver = new IntersectionObserver(([entry]) => {
      this.visible = entry.isIntersecting;
      this.schedule();
    });
    this.intersectionObserver.observe(host);

    window.addEventListener("pointermove", this.onPointerMove, { passive: true });
    document.documentElement.addEventListener("pointerleave", this.onPointerLeave);
    document.addEventListener("visibilitychange", this.schedule);
    this.schedule();
  }

  setMood(mood: CatMood) {
    this.mood = mood;
  }

  shake() {
    this.shakeStart = this.elapsed;
  }

  hop() {
    this.hopStart = this.elapsed;
  }

  dispose() {
    cancelAnimationFrame(this.frame);
    window.removeEventListener("pointermove", this.onPointerMove);
    document.documentElement.removeEventListener("pointerleave", this.onPointerLeave);
    document.removeEventListener("visibilitychange", this.schedule);
    this.resizeObserver.disconnect();
    this.intersectionObserver.disconnect();
    this.scene.environment?.dispose();
    for (const item of this.disposables) item.dispose();
    this.renderer.dispose();
    this.renderer.domElement.remove();
  }

  private readonly onPointerMove = (event: PointerEvent) => {
    this.pointer = { x: event.clientX, y: event.clientY };
  };

  private readonly onPointerLeave = () => {
    this.pointer = null;
  };

  private readonly schedule = () => {
    cancelAnimationFrame(this.frame);
    if (!this.visible || document.visibilityState === "hidden") return;
    this.lastTime = performance.now();
    this.frame = requestAnimationFrame(this.tick);
  };

  private readonly tick = (now: number) => {
    const dt = Math.min(0.05, (now - this.lastTime) / 1000);
    this.lastTime = now;
    this.elapsed += dt;
    this.animate(dt);
    this.renderer.render(this.scene, this.camera);
    if (!this.drawn) {
      this.drawn = true;
      this.options.onReady?.();
    }
    this.frame = requestAnimationFrame(this.tick);
  };

  private resize() {
    const { clientWidth: width, clientHeight: height } = this.host;
    if (!width || !height) return;
    this.renderer.setSize(width, height, false);
    this.camera.aspect = width / height;
    this.camera.updateProjectionMatrix();
  }

  private material(color: string, extra: Partial<ConstructorParameters<typeof MeshPhysicalMaterial>[0]> = {}): MeshPhysicalMaterial {
    const material = new MeshPhysicalMaterial({ color: new Color(color), roughness: 0.68, ...extra });
    this.disposables.push(material);
    return material;
  }

  private mesh(geometry: BufferGeometry, material: Material, parent: Group, shadow = true): Mesh {
    this.disposables.push(geometry);
    const mesh = new Mesh(geometry, material);
    mesh.castShadow = shadow;
    parent.add(mesh);
    return mesh;
  }

  private addLights() {
    this.scene.add(new HemisphereLight("#fffaf6", "#c8bfd2", 0.75));

    const key = new DirectionalLight("#fff6ec", 2.2);
    key.position.copy(KEY_LIGHT);
    key.castShadow = true;
    key.shadow.mapSize.set(1024, 1024);
    key.shadow.radius = 6;
    key.shadow.bias = -0.0008;
    Object.assign(key.shadow.camera, { left: -3, right: 3, top: 4, bottom: -1, near: 1, far: 20 });
    this.scene.add(key);

    const rim = new DirectionalLight("#efe4ff", 1.3);
    rim.position.set(-5, 4, -5);
    this.scene.add(rim);

    const groundGeometry = new PlaneGeometry(8, 8);
    const ground = new Mesh(groundGeometry, new ShadowMaterial({ opacity: 0.16 }));
    ground.rotation.x = -Math.PI / 2;
    ground.receiveShadow = true;
    this.disposables.push(groundGeometry, ground.material as Material);
    this.scene.add(ground);

    const contactTexture = contactShadowTexture();
    const contactMaterial = new MeshBasicMaterial({ map: contactTexture, transparent: true, depthWrite: false });
    const contactGeometry = new PlaneGeometry(3.6, 2.6);
    const contact = new Mesh(contactGeometry, contactMaterial);
    contact.rotation.x = -Math.PI / 2;
    contact.position.set(0.15, this.options.scenery ? CUSHION_SEAT + 0.08 : 0.005, 0.1);
    this.disposables.push(contactTexture, contactMaterial, contactGeometry);
    this.scene.add(contact);
  }

  /** A petal cushion to sit on and a ball of yarn it bats at now and then. */
  private buildScenery() {
    const cushion = this.material(CUSHION, { sheen: 1, sheenColor: new Color("#f3ecff"), sheenRoughness: 0.6, roughness: 0.8 });
    const pad = this.mesh(new SphereGeometry(1, 64, 32), cushion, this.scenery);
    pad.position.y = 0.2;
    pad.scale.set(1.5, 0.24, 1.2);
    pad.receiveShadow = true;
    for (let i = 0; i < 10; i++) {
      const angle = (i / 10) * Math.PI * 2;
      const petal = this.mesh(new SphereGeometry(0.55, 40, 24), cushion, this.scenery);
      petal.position.set(Math.cos(angle) * 1.32, 0.2, Math.sin(angle) * 1.05);
      petal.scale.set(1, 0.42, 0.9);
      petal.rotation.y = -angle;
      petal.receiveShadow = true;
    }

    // Yarn: pastel patches with the grooves of wound strands, lightly fuzzed.
    const yarnColors = ["#f4a6c1", "#9fcaf1", "#f6d77a", "#a9e2c8"].map((hex) => new Color(hex));
    const grooves = [new Vector3(1, 0.3, 0.2), new Vector3(-0.2, 1, 0.4), new Vector3(0.3, -0.4, 1)].map((v) => v.normalize());
    const yarnGeometry = new SphereGeometry(YARN_RADIUS, 64, 48);
    paint(yarnGeometry, (p) => {
      const n = p.clone().normalize();
      const patch = Math.floor((Math.atan2(n.z, n.x) / (Math.PI * 2) + 0.5) * 3 + (n.y + 1) * 1.5) % yarnColors.length;
      const family = Math.floor(((n.x + n.y * 2 + 3) / 6) * grooves.length) % grooves.length;
      const groove = Math.abs(Math.sin(n.dot(grooves[family]) * 40));
      return yarnColors[patch].clone().multiplyScalar(0.72 + 0.28 * smoothstep(groove, 0.15, 0.6));
    });
    const yarnMaterial = this.material("#ffffff", { vertexColors: true, sheen: 1, sheenColor: new Color("#ffffff"), sheenRoughness: 0.6, roughness: 0.9 });
    this.yarn.position.copy(YARN_HOME);
    this.scenery.add(this.yarn);
    const ball = this.mesh(yarnGeometry, yarnMaterial, this.yarn);
    this.fur(ball, { length: 0.035, density: [90, 45], layers: 8 });
  }

  private buildCat() {
    const white = new Color(WHITE);
    const grey = new Color(GREY);
    const dark = new Color(TABBY);
    const painted = this.material("#ffffff", { vertexColors: true, sheen: 1, sheenColor: new Color("#ffffff"), sheenRoughness: 0.7, roughness: 0.85 });
    const furWhite = this.material(WHITE, { sheen: 1, sheenColor: new Color("#ffffff"), sheenRoughness: 0.7, roughness: 0.85 });
    const furGrey = this.material("#c9c3be", { sheen: 1, sheenColor: new Color("#ffffff"), sheenRoughness: 0.7, roughness: 0.85 });
    const innerEar = this.material(INNER_EAR, { roughness: 0.55, sheen: 0.6, sheenColor: new Color("#ffd6d6") });
    const nose = this.material("#ee9aa2", { roughness: 0.35, clearcoat: 0.5 });
    const iris = this.material(IRIS, { roughness: 0.05, clearcoat: 1, clearcoatRoughness: 0.03 });
    const pupil = this.material("#0f0d0c", { roughness: 0.04, clearcoat: 1, clearcoatRoughness: 0.02 });
    const mouth = this.material("#8a6a62", { roughness: 0.6 });
    const whisker = this.material("#ffffff", { roughness: 0.4 });
    const collar = this.material(COLLAR, { roughness: 0.35, clearcoat: 0.6, sheen: 0.5, sheenColor: new Color("#efe6ff") });
    const tag = this.material("#dcb8b1", { metalness: 0.75, roughness: 0.28, clearcoat: 0.8 });
    const glint = new MeshBasicMaterial({ color: "#ffffff" });
    this.disposables.push(glint);

    this.root.add(this.body);

    // Body: a sitting pear turned on a lathe. White chest and legs, a grey
    // tabby saddle across the back, all painted into its vertex colours.
    const profile = new SplineCurve([
      new Vector2(0, 0), new Vector2(0.95, 0.04), new Vector2(1.18, 0.38), new Vector2(1.12, 0.85),
      new Vector2(0.92, 1.3), new Vector2(0.66, 1.66), new Vector2(0.4, 1.9), new Vector2(0, 1.98),
    ]).getPoints(40);
    const torsoGeometry = new LatheGeometry(profile, 72);
    paint(torsoGeometry, (p) => {
      const front = p.z / (Math.hypot(p.x, p.z) || 1);
      const saddle = (1 - smoothstep(front, -0.55, 0.05)) * smoothstep(p.y, 0.45, 0.85);
      const bands = smoothstep(Math.sin(p.y * 9 + Math.atan2(p.x, -p.z) * 2), 0.55, 0.9) * saddle;
      return new Color().lerpColors(white, grey, saddle * 0.9).lerp(dark, bands * 0.6);
    });
    const torso = this.mesh(torsoGeometry, painted, this.body);
    torso.scale.set(1, 1, 0.88);
    torso.receiveShadow = true;
    this.fur(torso, { length: 0.1, density: [300, 110], layers: 18 });

    for (const side of [-1, 1]) {
      const footGeometry = new SphereGeometry(0.34, 40, 24);
      paint(footGeometry, () => white.clone());
      const foot = this.mesh(footGeometry, painted, this.body);
      foot.position.set(side * 0.78, 0.13, 0.55);
      foot.scale.set(1, 0.5, 1.35);
      this.fur(foot, { length: 0.05, density: [80, 40], layers: 10 });
    }

    // Collar and heart tag sit on the body, so the head turns above them.
    const ring = this.mesh(new TorusGeometry(0.76, 0.09, 18, 72), collar, this.body);
    ring.position.set(0, 1.58, 0.02);
    ring.rotation.x = Math.PI / 2 - 0.12;
    ring.scale.y = 0.9;
    const loop = this.mesh(new TorusGeometry(0.05, 0.014, 10, 24), tag, this.body, false);
    loop.position.set(0, 1.5, 0.8);
    const heart = new Shape();
    heart.moveTo(0, -0.1);
    heart.bezierCurveTo(-0.02, -0.07, -0.13, -0.02, -0.12, 0.05);
    heart.bezierCurveTo(-0.11, 0.11, -0.03, 0.12, 0, 0.06);
    heart.bezierCurveTo(0.03, 0.12, 0.11, 0.11, 0.12, 0.05);
    heart.bezierCurveTo(0.13, -0.02, 0.02, -0.07, 0, -0.1);
    const tagMesh = this.mesh(new ExtrudeGeometry(heart, { depth: 0.03, bevelEnabled: true, bevelThickness: 0.015, bevelSize: 0.015, bevelSegments: 4, curveSegments: 16 }), tag, this.body);
    tagMesh.position.set(0, 1.37, 0.84);
    tagMesh.rotation.x = -0.25;

    // Tail: a fluffy grey tube with darker rings, swaying from its root.
    this.tail.position.set(0.55, 0.22, -0.7);
    this.body.add(this.tail);
    const tailCurve = new CatmullRomCurve3([
      new Vector3(0, 0, 0), new Vector3(0.75, 0.05, 0.15), new Vector3(1.15, 0.5, 0.55), new Vector3(1.05, 1.05, 0.85), new Vector3(0.82, 1.32, 0.95),
    ]);
    const tailGeometry = new TubeGeometry(tailCurve, 72, 0.2, 20, false);
    paint(tailGeometry, (_, uv) => new Color().lerpColors(grey, dark, smoothstep(Math.sin(uv.x * 30), 0.45, 0.85) * 0.55));
    const tailMesh = this.mesh(tailGeometry, painted, this.tail);
    this.fur(tailMesh, { length: 0.12, density: [130, 50], layers: 16 });
    const tailTipGeometry = new SphereGeometry(0.2, 32, 20);
    paint(tailTipGeometry, () => grey.clone());
    const tailTip = this.mesh(tailTipGeometry, painted, this.tail);
    tailTip.position.copy(tailCurve.getPoint(1));
    this.fur(tailTip, { length: 0.12, density: [50, 26], layers: 16 });

    // Head, pivoting at the neck so turning it reads as a real head turn.
    this.headPivot.position.set(0, 1.78, 0.05);
    this.root.add(this.headPivot);
    const head = new Group();
    head.position.set(0, 0.66, 0);
    this.headPivot.add(head);

    // Grey tabby cap with darker stripes, grey patches around the eyes, a
    // white blaze down the nose and a white muzzle and chin.
    const skullGeometry = new SphereGeometry(1, 96, 72);
    paint(skullGeometry, (p) => {
      const cap = smoothstep(p.y, 0.28, 0.55);
      const eyePatch = 1 - smoothstep(Math.hypot(Math.abs(p.x) - 0.5, p.y - 0.18, p.z - 0.72), 0.22, 0.42);
      const blaze = (1 - smoothstep(Math.abs(p.x), 0.07, 0.17)) * (1 - smoothstep(p.y, 0.4, 0.78)) * smoothstep(p.z, 0.35, 0.65);
      const lowerFace = smoothstep(-p.y, -0.08, 0.2);
      const patch = Math.max(cap, eyePatch) * (1 - blaze) * (1 - lowerFace);
      const stripe = Math.min(Math.abs(p.x - 0.17), Math.abs(p.x + 0.17), Math.abs(p.x - 0.4), Math.abs(p.x + 0.4));
      const stripes = (1 - smoothstep(stripe, 0.03, 0.07)) * smoothstep(p.y, 0.42, 0.6) * smoothstep(p.z, -0.3, 0.2);
      return new Color().lerpColors(white, grey, patch).lerp(dark, stripes * patch * 0.75);
    });
    // Short fur on the face so the eyes and nose stay clear; full fluff on the cheeks and crown.
    setFurScale(skullGeometry, (p) => {
      const face = smoothstep(p.z, 0.5, 0.8) * (1 - smoothstep(Math.abs(p.y + 0.02), 0.3, 0.55));
      return 1 - 0.85 * face;
    });
    const skull = this.mesh(skullGeometry, painted, head);
    skull.scale.set(1.2, 0.98, 1.0);
    skull.receiveShadow = true;
    this.fur(skull, { length: 0.09, density: [360, 140], layers: 18 });

    for (const side of [-1, 1]) {
      const muzzle = this.mesh(new SphereGeometry(0.29, 32, 24), furWhite, head);
      muzzle.position.set(side * 0.19, -0.3, 0.84);
      muzzle.scale.set(1.05, 0.8, 0.75);
    }
    const chin = this.mesh(new SphereGeometry(0.19, 24, 16), furWhite, head);
    chin.position.set(0, -0.47, 0.76);
    chin.scale.set(1.1, 0.7, 0.8);

    const noseTip = this.mesh(new SphereGeometry(0.085, 24, 16), nose, head);
    noseTip.position.set(0, -0.15, 1.01);
    noseTip.scale.set(1.35, 0.8, 0.85);

    for (const side of [-1, 1]) {
      const lip = this.mesh(new TorusGeometry(0.075, 0.013, 8, 24, Math.PI), mouth, head, false);
      lip.position.set(side * 0.075, -0.28, 1.03);
      lip.rotation.set(-0.25, 0, Math.PI);

      for (const [tilt, drop] of [[0.12, 0], [-0.08, 0.07]] as const) {
        const hair = this.mesh(new CapsuleGeometry(0.008, 0.7, 4, 6), whisker, head, false);
        hair.position.set(side * 0.64, -0.29 - drop, 0.84);
        hair.rotation.set(0, side * -0.35, Math.PI / 2 + side * tilt);
      }

      // Big glossy eyes: a grey-green iris dome with a wide pupil and two catchlights.
      const eyeGroup = new Group();
      eyeGroup.position.set(side * 0.43, 0.07, 0.83);
      head.add(eyeGroup);
      const irisMesh = this.mesh(new SphereGeometry(0.23, 48, 32), iris, eyeGroup, false);
      irisMesh.scale.set(1, 1.08, 0.62);
      const pupilMesh = this.mesh(new SphereGeometry(0.16, 40, 28), pupil, eyeGroup, false);
      pupilMesh.position.z = 0.055;
      pupilMesh.scale.set(1, 1.06, 0.62);
      const sparkle = this.mesh(new SphereGeometry(0.055, 16, 12), glint, eyeGroup, false);
      sparkle.position.set(0.07, 0.09, 0.15);
      const sparkleSmall = this.mesh(new SphereGeometry(0.026, 12, 10), glint, eyeGroup, false);
      sparkleSmall.position.set(-0.07, -0.08, 0.15);
      this.eyes.push(eyeGroup);

      const ear = new Group();
      ear.position.set(side * 0.64, 0.7, 0.08);
      ear.rotation.set(-0.08, 0, side * -0.36);
      head.add(ear);
      const outer = this.mesh(new ConeGeometry(0.44, 0.86, 40, 1), furGrey, ear);
      outer.position.y = 0.34;
      outer.scale.z = 0.72;
      const inner = this.mesh(new ConeGeometry(0.29, 0.6, 40, 1), innerEar, ear, false);
      inner.position.set(0, 0.28, 0.15);
      inner.scale.z = 0.4;
      this.ears.push(ear);
    }

    // Front legs: each one smooth turned shape from a chubby shoulder down to a
    // round paw, its top buried in the chest so no joint shows. The gaps
    // between the toes are painted on rather than glued on.
    const legProfile = new SplineCurve([
      new Vector2(0, -LEG_LENGTH - 0.27), new Vector2(0.2, -LEG_LENGTH - 0.24), new Vector2(0.3, -LEG_LENGTH - 0.08),
      new Vector2(0.29, -LEG_LENGTH + 0.1), new Vector2(0.2, -LEG_LENGTH + 0.32), new Vector2(0.21, -0.75),
      new Vector2(0.26, -0.3), new Vector2(0.27, 0), new Vector2(0.18, 0.2), new Vector2(0, 0.26),
    ]).getPoints(60);
    const toeGap = new Color("#cfc6bf");
    for (const side of [-1, 1]) {
      const legGeometry = new LatheGeometry(legProfile, 48);
      paint(legGeometry, (p) => {
        const angle = Math.atan2(p.x, p.z);
        const gap = Math.min(Math.abs(angle - 0.36), Math.abs(angle), Math.abs(angle + 0.36));
        const toes = (1 - smoothstep(gap, 0.035, 0.07)) * smoothstep(-p.y, LEG_LENGTH + 0.02, LEG_LENGTH + 0.1) * (1 - smoothstep(-p.y, LEG_LENGTH + 0.2, LEG_LENGTH + 0.25));
        return new Color().lerpColors(white, toeGap, toes);
      });
      setFurScale(legGeometry, (p) => 1 - 0.7 * smoothstep(-p.y, LEG_LENGTH - 0.2, LEG_LENGTH));
      const arm = new Group();
      arm.position.set(side * 0.42, 1.35, 0.62);
      arm.rotation.x = ARM_REST;
      this.root.add(arm);
      const leg = this.mesh(legGeometry, painted, arm);
      leg.scale.z = 0.92;
      this.fur(leg, { length: 0.07, density: [70, 70], layers: 14 });
      this.arms.push(arm);
    }
  }

  private fur(base: Mesh, options: FurOptions) {
    const { material } = growFur(base, options);
    this.disposables.push(material);
  }

  private gazeTarget(): { yaw: number; pitch: number } {
    if (this.mood === "shy") return { yaw: 0, pitch: 0.28 };
    // Eyes on the yarn from the moment it reaches out until the ball comes back.
    if (this.tapStart >= 0) return { yaw: -0.5, pitch: 0.38 };
    const target = this.options.focus?.() ?? this.pointer;
    if (!target) return { yaw: 0, pitch: 0 };
    const box = this.host.getBoundingClientRect();
    const dx = target.x - (box.left + box.width / 2);
    const dy = target.y - (box.top + box.height * 0.3);
    const reach = this.options.reach;
    return {
      yaw: clamp(Math.atan2(dx, reach) * 1.1, -MAX_YAW, MAX_YAW),
      pitch: clamp(Math.atan2(dy, reach) * 0.9, MIN_PITCH, MAX_PITCH),
    };
  }

  /** Now and then, while idle, swat the yarn: it rolls off and drifts back. */
  private playWithYarn(t: number) {
    if (!this.options.scenery || this.reducedMotion) return;
    if (this.tapStart < 0) {
      if (this.mood === "idle" && t >= this.nextTap) this.tapStart = t;
      else return;
    }
    const elapsed = t - this.tapStart;

    // The near paw: up and out, down onto the ball, then home. Writing the
    // angle back into armAngles lets the usual damping take over afterwards.
    if (this.mood === "idle" && elapsed < TAP_DURATION) {
      const arm = this.arms[0];
      const reach = smoothstep(elapsed, 0, 0.35);
      const strike = smoothstep(elapsed, 0.35, TAP_STRIKE);
      const settle = smoothstep(elapsed, TAP_STRIKE + 0.1, TAP_DURATION);
      const raised = ARM_REST + (-1.0 - ARM_REST) * reach + (ARM_REST + 1.0) * strike;
      this.armAngles[0] = raised + (ARM_REST - raised) * settle;
      arm.rotation.x = this.armAngles[0];
      arm.rotation.z = (-0.45 * reach - 0.17 * strike) * (1 - settle);
    }

    const sinceHit = elapsed - TAP_STRIKE;
    let offset = 0;
    if (sinceHit > 0 && sinceHit < ROLL_OUT) offset = -ROLL_DISTANCE * Math.sin((sinceHit / ROLL_OUT) * (Math.PI / 2));
    else if (sinceHit >= ROLL_OUT) offset = -ROLL_DISTANCE * (1 - smoothstep(sinceHit, ROLL_BACK_START, ROLL_BACK_END));
    this.yarn.position.x = YARN_HOME.x + offset;
    this.yarn.rotation.z = -offset / YARN_RADIUS;

    if (sinceHit >= ROLL_BACK_END) {
      this.tapStart = -1;
      this.nextTap = t + 7 + Math.random() * 5;
    }
  }

  private animate(dt: number) {
    const t = this.elapsed;
    const gaze = this.gazeTarget();
    this.yaw = damp(this.yaw, gaze.yaw, 7, dt);
    this.pitch = damp(this.pitch, gaze.pitch, 7, dt);

    let yaw = this.yaw;
    if (this.shakeStart >= 0) {
      const progress = (t - this.shakeStart) / 0.7;
      if (progress >= 1) this.shakeStart = -1;
      else yaw += Math.sin(progress * Math.PI * 6) * 0.38 * (1 - progress);
    }
    this.headPivot.rotation.set(this.pitch, yaw, -yaw * 0.22);

    const armTargets: Record<CatMood, [number, number]> = {
      idle: [ARM_REST, ARM_REST],
      watch: [ARM_REST, ARM_REST],
      shy: [ARM_COVER, ARM_COVER],
      peek: [ARM_PEEK, ARM_COVER],
    };
    const targets = armTargets[this.mood];
    this.arms.forEach((arm, index) => {
      this.armAngles[index] = damp(this.armAngles[index], targets[index], 9, dt);
      arm.rotation.x = this.armAngles[index];
      // Raised paws splay outwards a little so they land on the eyes.
      const lift = (ARM_REST - this.armAngles[index]) / (ARM_REST - ARM_COVER);
      arm.rotation.z = (index === 0 ? -1 : 1) * 0.06 * lift;
    });
    this.playWithYarn(t);

    let hop = 0;
    if (this.hopStart >= 0) {
      const progress = (t - this.hopStart) / 0.5;
      if (progress >= 1) this.hopStart = -1;
      else hop = Math.sin(progress * Math.PI) * 0.4;
    }
    this.root.position.y = this.seat + hop;

    if (this.reducedMotion) return;

    this.body.scale.y = 1 + Math.sin(t * 2.1) * 0.012;
    this.headPivot.position.y = 1.78 + Math.sin(t * 2.1) * 0.012;
    this.tail.rotation.set(0, Math.sin(t * 1.3) * 0.12, Math.sin(t * (this.mood === "shy" ? 3.2 : 1.6)) * 0.16);

    if (t >= this.nextBlink && this.blinkStart < 0) this.blinkStart = t;
    let lid = 1;
    if (this.blinkStart >= 0) {
      const progress = (t - this.blinkStart) / 0.16;
      if (progress >= 1) {
        this.blinkStart = -1;
        this.nextBlink = t + 2.5 + Math.random() * 3.5;
      } else lid = 1 - Math.sin(progress * Math.PI) * 0.92;
    }
    for (const eye of this.eyes) eye.scale.y = lid;

    if (t >= this.nextTwitch && this.twitchStart < 0) this.twitchStart = t;
    let twitch = 0;
    if (this.twitchStart >= 0) {
      const progress = (t - this.twitchStart) / 0.3;
      if (progress >= 1) {
        this.twitchStart = -1;
        this.nextTwitch = t + 4 + Math.random() * 5;
      } else twitch = Math.sin(progress * Math.PI * 2) * 0.25;
    }
    this.ears[1].rotation.z = -0.36 + twitch;
  }
}
