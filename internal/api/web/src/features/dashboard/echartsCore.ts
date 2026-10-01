import type {
  EffectScatterSeriesOption,
  LineSeriesOption,
  LinesSeriesOption,
  ScatterSeriesOption,
} from "echarts/charts";
import type {
  GeoComponentOption,
  GridComponentOption,
  TooltipComponentOption,
  VisualMapComponentOption,
} from "echarts/components";
import * as echarts from "echarts/core";
import { EffectScatterChart, LineChart, LinesChart, ScatterChart } from "echarts/charts";
import { GeoComponent, GridComponent, TooltipComponent, VisualMapComponent } from "echarts/components";
import { CanvasRenderer } from "echarts/renderers";
import type { ComposeOption } from "echarts/core";

/**
 * The only module that imports ECharts.
 *
 * Registration is explicit, so the bundler ships the chart types this screen uses
 * instead of the whole library — and because only the lazily loaded chart
 * components import this file, nothing else in the panel pays for it.
 *
 * `GeoComponent` is what draws the world: it renders the registered outline and
 * carries the `geo` coordinate system the flight lines and hubs are placed on, so
 * a `map` series is not needed as well.
 */
echarts.use([
  LineChart,
  LinesChart,
  EffectScatterChart,
  ScatterChart,
  GeoComponent,
  GridComponent,
  TooltipComponent,
  VisualMapComponent,
  CanvasRenderer,
]);

export { echarts };

export type ChartOption = ComposeOption<
  | LineSeriesOption
  | LinesSeriesOption
  | EffectScatterSeriesOption
  | ScatterSeriesOption
  | GeoComponentOption
  | GridComponentOption
  | TooltipComponentOption
  | VisualMapComponentOption
>;

export type EChartsInstance = ReturnType<typeof echarts.init>;
