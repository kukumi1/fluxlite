import {
  BufferAttribute,
  InstancedMesh,
  MeshPhysicalMaterial,
  Vector2,
  Vector3,
  type BufferGeometry,
  type Mesh,
} from "three";

export interface FurOptions {
  /** Strand length in scene units at full `furScale`. */
  length: number;
  /** Strands per unit of uv, across and along. */
  density: [number, number];
  layers: number;
}

/** Per-vertex strand length (0…1), e.g. short fur around the eyes. Defaults to 1. */
export function setFurScale(geometry: BufferGeometry, scaleAt: (position: Vector3) => number) {
  const positions = geometry.getAttribute("position");
  const scales = new Float32Array(positions.count);
  const position = new Vector3();
  for (let i = 0; i < positions.count; i++) {
    position.set(positions.getX(i), positions.getY(i), positions.getZ(i));
    scales[i] = scaleAt(position);
  }
  geometry.setAttribute("furScale", new BufferAttribute(scales, 1));
}

/**
 * Shell fur: the mesh redrawn `layers` times, each copy pushed a little further
 * out along its normals and cut down to a field of tapering strands. One
 * instanced draw per furry part; the instance index is the shell height.
 *
 * The shells are the base mesh's own physical material with the strand cut
 * patched in, so they are lit exactly like the skin beneath. That is what keeps
 * fur facing the camera from reading as speckle: only the silhouette, where
 * strands stand against the background, shows as fluff.
 */
export function growFur(base: Mesh, options: FurOptions): { shells: InstancedMesh; material: MeshPhysicalMaterial } {
  const geometry = base.geometry;
  if (!geometry.getAttribute("furScale")) setFurScale(geometry, () => 1);
  const material = (base.material as MeshPhysicalMaterial).clone();
  const uniforms = {
    uFurLength: { value: options.length },
    uFurLayers: { value: options.layers },
    uFurDensity: { value: new Vector2(...options.density) },
  };
  material.onBeforeCompile = (shader) => {
    Object.assign(shader.uniforms, uniforms);
    shader.vertexShader = shader.vertexShader
      .replace(
        "#include <common>",
        `#include <common>
        attribute float furScale;
        uniform float uFurLength;
        uniform float uFurLayers;
        varying vec2 vFurUv;
        varying float vFurHeight;`,
      )
      .replace(
        "#include <begin_vertex>",
        `#include <begin_vertex>
        vFurHeight = (float(gl_InstanceID) + 1.0) / uFurLayers;
        float furReach = uFurLength * furScale * vFurHeight;
        transformed += objectNormal * furReach;
        // Longer strands sag a little, which is most of what reads as soft.
        transformed.y -= furReach * vFurHeight * 0.45;
        vFurUv = uv;`,
      );
    shader.fragmentShader = shader.fragmentShader
      .replace(
        "#include <common>",
        `#include <common>
        uniform vec2 uFurDensity;
        varying vec2 vFurUv;
        varying float vFurHeight;
        float furHash(vec2 p) {
          return fract(sin(dot(p, vec2(127.1, 311.7))) * 43758.5453);
        }`,
      )
      .replace(
        "#include <clipping_planes_fragment>",
        `#include <clipping_planes_fragment>
        vec2 furCell = vFurUv * uFurDensity;
        // Each strand is a cone: thick at the root, gone by its own random height.
        float furThickness = 1.8 * (furHash(floor(furCell)) - vFurHeight);
        if (furThickness <= 0.0 || length(fract(furCell) - 0.5) > furThickness) discard;`,
      )
      .replace(
        "#include <color_fragment>",
        `#include <color_fragment>
        diffuseColor.rgb *= mix(0.86, 1.0, vFurHeight);`,
      );
  };
  material.customProgramCacheKey = () => "cat-fur";
  const shells = new InstancedMesh(geometry, material, options.layers);
  shells.frustumCulled = false;
  base.add(shells);
  return { shells, material };
}
