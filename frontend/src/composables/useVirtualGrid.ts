import { ref, computed, onBeforeUnmount } from 'vue'

export interface VirtualGridOptions {
  columns: number
  gap: number
  itemHeight: number
  containerHeight: number
  overscan?: number
}

export interface VirtualGridItem<T> {
  data: T
  index: number
  row: number
  col: number
}

export function useVirtualGrid<T>(
  items: () => T[],
  options: () => VirtualGridOptions
) {
  const scrollTop = ref(0)
  let scrollRAF: number | null = null

  const opts = computed(() => options())
  const itemList = computed(() => items())

  const rowHeight = computed(() => opts.value.itemHeight + opts.value.gap)
  const totalRows = computed(() => Math.ceil(itemList.value.length / opts.value.columns))

  const totalHeight = computed(() => {
    if (totalRows.value === 0) return 0
    return totalRows.value * opts.value.itemHeight + (totalRows.value - 1) * opts.value.gap
  })

  const visibleRange = computed(() => {
    if (rowHeight.value <= 0) return { startRow: 0, endRow: 0 }
    const overscan = opts.value.overscan ?? 3
    const startRow = Math.max(0, Math.floor(scrollTop.value / rowHeight.value) - overscan)
    const endRow = Math.min(
      totalRows.value,
      Math.ceil((scrollTop.value + opts.value.containerHeight) / rowHeight.value) + overscan
    )
    return { startRow, endRow }
  })

  const visibleItems = computed((): VirtualGridItem<T>[] => {
    const { startRow, endRow } = visibleRange.value
    const result: VirtualGridItem<T>[] = []
    const cols = opts.value.columns

    for (let row = startRow; row < endRow; row++) {
      for (let col = 0; col < cols; col++) {
        const index = row * cols + col
        if (index < itemList.value.length) {
          result.push({
            data: itemList.value[index],
            index,
            row,
            col
          })
        }
      }
    }
    return result
  })

  function handleScroll(e: Event) {
    if (scrollRAF) return
    scrollRAF = requestAnimationFrame(() => {
      const target = e.target as HTMLElement
      scrollTop.value = target.scrollTop
      scrollRAF = null
    })
  }

  function resetScroll() {
    scrollTop.value = 0
  }

  function cleanup() {
    if (scrollRAF) cancelAnimationFrame(scrollRAF)
  }

  onBeforeUnmount(cleanup)

  return {
    scrollTop,
    totalHeight,
    rowHeight,
    visibleItems,
    handleScroll,
    resetScroll,
    cleanup
  }
}
