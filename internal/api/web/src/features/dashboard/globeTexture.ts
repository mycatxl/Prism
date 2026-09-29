import type { GlobePalette } from "./chartPalette";
import type { WorldGeoJson, WorldGeometry, WorldRing } from "./worldMap";

/**
 * The earth, painted from the geometry the panel already ships.
 *
 * Nothing is fetched and nothing is added to `public/`: `world-110m.geo.json` is
 * the same committed feature collection the flat map registers with ECharts, so
 * the coastline on the sphere and the coastline on the paper map can never drift
 * apart, and the wall display keeps its promise of talking to nobody but its own
 * backend.
 *
 * 2048x1024 is 0.176 degrees — about 19 km at the equator — per pixel, which is
 * beyond what a sphere drawn two-thirds of a panel high can resolve.
 *
 * The orientation is the whole risk in a texture built by hand, so it is written
 * down: `Globe.dataToPoint` puts longitude L at (cos L, sin L) on the (x, z)
 * plane, which is texture u = (L + 180) / 360, and claygl's sphere carries v = 0
 * at the north pole with the texture loaded `flipY: false`, which is row 0 of
 * this canvas. Longitude -180 is therefore the left edge, latitude 90 the top
 * row: a plain equirectangular plate, north up.
 */
export const GLOBE_TEXTURE_WIDTH = 2048;
export const GLOBE_TEXTURE_HEIGHT = 1024;

/** Every 30 degrees, drawn under the land so it only crosses open water. */
const GRATICULE_STEP_DEGREES = 30;

/**
 * Paint the world into an offscreen canvas for a globe material's `baseTexture`.
 *
 * Throws if the browser has no 2d context at all; the caller treats that as one
 * more reason to hand the panel back to the flat map.
 */
export function buildGlobeTexture(geo: WorldGeoJson, palette: GlobePalette): HTMLCanvasElement {
  const canvas = document.createElement("canvas");
  canvas.width = GLOBE_TEXTURE_WIDTH;
  canvas.height = GLOBE_TEXTURE_HEIGHT;

  const context = canvas.getContext("2d");
  if (!context) {
    throw new Error("world-110m.geo.json: this browser has no 2d canvas context");
  }

  context.fillStyle = palette.base;
  context.fillRect(0, 0, GLOBE_TEXTURE_WIDTH, GLOBE_TEXTURE_HEIGHT);

  drawGraticule(context, palette);

  context.fillStyle = palette.land;
  context.strokeStyle = palette.coast;
  context.lineWidth = 1;

  for (const feature of geo.features) {
    const geometry = feature.geometry;
    if (!geometry) {
      continue;
    }
    context.beginPath();
    for (const ring of ringsOf(geometry)) {
      traceRing(context, ring);
    }
    // evenodd, so the holes GeoJSON cuts into a country stay water: a marker
    // drawn over the Caspian must not read as a country that exists there.
    context.fill("evenodd");
    context.stroke();
  }

  return canvas;
}

/** Longitude in degrees to a pixel column, -180 on the left edge. */
function projectX(lon: number): number {
  return ((lon + 180) / 360) * GLOBE_TEXTURE_WIDTH;
}

/** Latitude in degrees to a pixel row, +90 on the top row. */
function projectY(lat: number): number {
  return ((90 - lat) / 180) * GLOBE_TEXTURE_HEIGHT;
}

/** Every ring of the geometry, outlines and holes alike: the fill needs both. */
function ringsOf(geometry: WorldGeometry): WorldRing[] {
  if (geometry.type === "Polygon") {
    return geometry.coordinates;
  }
  const rings: WorldRing[] = [];
  for (const polygon of geometry.coordinates) {
    for (const ring of polygon) {
      rings.push(ring);
    }
  }
  return rings;
}

function traceRing(context: CanvasRenderingContext2D, ring: WorldRing): void {
  if (ring.length === 0) {
    return;
  }
  for (let index = 0; index < ring.length; index += 1) {
    const [lon, lat] = ring[index];
    const x = projectX(lon);
    const y = projectY(lat);
    if (index === 0) {
      context.moveTo(x, y);
    } else {
      context.lineTo(x, y);
    }
  }
  context.closePath();
}

/**
 * The latitude and longitude lines, on the ocean only.
 *
 * Placed on half pixels, and drawn wider than a pixel: this plate is handed to
 * the sphere at 2048 wide and drawn about 300 wide, so a 1px line arrives as a
 * seventh of a pixel and disappears into the sea. Three pixels survives the
 * minification as the faint grid it is meant to be.
 */
function drawGraticule(context: CanvasRenderingContext2D, palette: GlobePalette): void {
  context.save();
  context.strokeStyle = palette.graticule;
  context.lineWidth = 3;
  context.beginPath();

  for (let lon = -180 + GRATICULE_STEP_DEGREES; lon < 180; lon += GRATICULE_STEP_DEGREES) {
    const x = Math.round(projectX(lon)) + 0.5;
    context.moveTo(x, 0);
    context.lineTo(x, GLOBE_TEXTURE_HEIGHT);
  }
  for (let lat = 90 - GRATICULE_STEP_DEGREES; lat > -90; lat -= GRATICULE_STEP_DEGREES) {
    const y = Math.round(projectY(lat)) + 0.5;
    context.moveTo(0, y);
    context.lineTo(GLOBE_TEXTURE_WIDTH, y);
  }

  context.stroke();
  context.restore();
}

/**
 * The same plate as a data URL.
 *
 * echarts-gl takes a canvas directly, but only on the call that populates its
 * texture cache: every later `setTextureImage` for the same canvas disables the
 * material's `diffuseMap`, finds the cached texture and never re-enables it, so
 * the sphere renders in the material's flat base colour (white) after the second
 * render — which is any resize or data refresh. Its string branch re-runs the
 * loader callback on every cache hit, so the texture stays bound, and a data URL
 * costs no request. Hence: canvas in, data URL out.
 */
export function globeTextureDataUrl(geo: WorldGeoJson, palette: GlobePalette): string {
  return buildGlobeTexture(geo, palette).toDataURL("image/png");
}
