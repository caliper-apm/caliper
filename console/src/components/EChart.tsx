import { useEffect, useRef, useState } from 'react';
import * as echarts from 'echarts/core';
import { LineChart, BarChart, PieChart } from 'echarts/charts';
import {
  GridComponent,
  TooltipComponent,
  LegendComponent,
  MarkLineComponent,
  MarkAreaComponent,
} from 'echarts/components';
import { CanvasRenderer } from 'echarts/renderers';
import type { ECBasicOption } from 'echarts/types/dist/shared';

echarts.use([
  LineChart,
  BarChart,
  PieChart,
  GridComponent,
  TooltipComponent,
  LegendComponent,
  MarkLineComponent,
  MarkAreaComponent,
  CanvasRenderer,
]);

const LIGHT_TO_DARK_COLOR: Record<string, string> = {
  '#f2f3f5': '#263042',
  '#e5e6eb': '#2e3a50',
  '#4e5969': '#b4bfd1',
  '#1d2129': '#e8edf5',
  '#86909c': '#94a3b8',
};

function adaptOptionForDark(node: unknown): unknown {
  if (Array.isArray(node)) return node.map(adaptOptionForDark);
  if (!node || typeof node !== 'object') {
    if (typeof node === 'string') {
      const mapped = LIGHT_TO_DARK_COLOR[node.toLowerCase()];
      return mapped ?? node;
    }
    return node;
  }
  const src = node as Record<string, unknown>;
  const out: Record<string, unknown> = {};
  for (const [k, v] of Object.entries(src)) {
    out[k] = adaptOptionForDark(v);
  }
  return out;
}

function applyDarkTheme(option: ECBasicOption): ECBasicOption {
  const cloned = adaptOptionForDark(option) as Record<string, unknown>;
  cloned.textStyle = { color: '#b4bfd1', ...(cloned.textStyle as Record<string, unknown> | undefined) };
  if (cloned.tooltip && typeof cloned.tooltip === 'object' && !Array.isArray(cloned.tooltip)) {
    cloned.tooltip = {
      backgroundColor: '#1e2533',
      borderColor: '#334057',
      textStyle: { color: '#e8edf5' },
      ...(cloned.tooltip as Record<string, unknown>),
    };
  }
  if (cloned.legend && typeof cloned.legend === 'object' && !Array.isArray(cloned.legend)) {
    cloned.legend = {
      textStyle: { color: '#b4bfd1' },
      ...(cloned.legend as Record<string, unknown>),
    };
  }
  return cloned as ECBasicOption;
}

function useIsDarkTheme(): boolean {
  const [dark, setDark] = useState(() => document.documentElement.dataset.theme === 'dark');
  useEffect(() => {
    const mo = new MutationObserver(() => {
      setDark(document.documentElement.dataset.theme === 'dark');
    });
    mo.observe(document.documentElement, { attributes: true, attributeFilter: ['data-theme'] });
    return () => mo.disconnect();
  }, []);
  return dark;
}

interface Props {
  option: ECBasicOption;
  height?: number;
  className?: string;
}

export function EChart({ option, height = 260, className }: Props) {
  const hostRef = useRef<HTMLDivElement | null>(null);
  const instRef = useRef<echarts.ECharts | null>(null);
  const isDark = useIsDarkTheme();

  useEffect(() => {
    const el = hostRef.current;
    if (!el) return;
    const chart = echarts.init(el, undefined, { renderer: 'canvas' });
    instRef.current = chart;

    const ro = new ResizeObserver(() => {
      chart.resize();
    });
    ro.observe(el);

    return () => {
      ro.disconnect();
      chart.dispose();
      instRef.current = null;
    };
  }, []);

  useEffect(() => {
    const themed = isDark ? applyDarkTheme(option) : option;
    instRef.current?.setOption(themed, { notMerge: true, lazyUpdate: true });
  }, [option, isDark]);

  return <div ref={hostRef} className={className} style={{ width: '100%', height }} />;
}

interface SparkProps {
  values: Array<number | null>;
  color: string;
  height?: number;
}

export function Sparkline({ values, color, height = 38 }: SparkProps) {
  const option: ECBasicOption = {
    animation: false,
    grid: { top: 4, right: 2, bottom: 2, left: 2 },
    xAxis: { type: 'category', show: false, data: values.map((_, i) => i) },
    yAxis: { type: 'value', show: false, scale: true },
    series: [
      {
        type: 'line',
        data: values,
        connectNulls: false,
        showSymbol: false,
        smooth: 0.25,
        lineStyle: { width: 2, color },
        areaStyle: {
          color: {
            type: 'linear',
            x: 0, y: 0, x2: 0, y2: 1,
            colorStops: [
              { offset: 0, color: `${color}33` },
              { offset: 1, color: `${color}03` },
            ],
          },
        },
      },
    ],
  };
  return <EChart option={option} height={height} />;
}
