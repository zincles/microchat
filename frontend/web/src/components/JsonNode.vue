<script setup lang="ts">
// 一个 JSON 树节点：对象/数组可展开，标量直接显示。
//
// 关键约束：**只有展开时才创建子节点**。一个 ST 预设是 1.5MB / 253 条 prompts，
// 一上来把整棵树铺开会把浏览器按在地上；所以默认只看根的直接键，点一层开一层。
import { computed, ref } from "vue";
import { isJsonContainer, jsonEntries, jsonKind, jsonPreview, type JsonValue } from "../utils/json.ts";

const props = withDefaults(defineProps<{
  name?: string;
  value: JsonValue;
  depth?: number;
  index?: boolean;
  openInitially?: boolean;
}>(), {
  depth: 0,
  index: false,
  openInitially: false,
});

const open = ref(props.openInitially);
const container = computed(() => isJsonContainer(props.value));
const array = computed(() => Array.isArray(props.value));
const kind = computed(() => jsonKind(props.value));
const entries = computed(() => container.value ? jsonEntries(props.value) : []);
const count = computed(() => entries.value.length);
const hasName = computed(() => props.name !== undefined);
const label = computed(() => props.index ? `[${props.name ?? ""}]` : JSON.stringify(props.name ?? ""));
const preview = computed(() => jsonPreview(props.value));
const longString = computed(() => typeof props.value === "string" && props.value.length > 240);
const expandedString = ref(false);
const fullString = computed(() => typeof props.value === "string" ? props.value : "");

const lineStyle = computed(() => ({ paddingLeft: `${0.2 + props.depth * 1.05}em` }));
const closeStyle = computed(() => ({ paddingLeft: `${1.25 + props.depth * 1.05}em` }));
const fullStyle = computed(() => ({ marginLeft: `${1.25 + props.depth * 1.05}em` }));
</script>

<template>
  <div class="json-node">
    <button
      v-if="container"
      type="button"
      class="json-line json-branch"
      :style="lineStyle"
      :aria-expanded="open"
      @click="open = !open"
    >
      <span class="json-twisty" aria-hidden="true">{{ open ? "▾" : "▸" }}</span>
      <span v-if="hasName" class="json-label" :class="{ 'json-index': index }">{{ label }}</span><span v-if="hasName" class="json-colon">:</span>
      <span class="json-punct">{{ array ? "[" : "{" }}</span>
      <span class="json-count">{{ count }}{{ array ? " 项" : " 键" }}</span>
      <span v-if="!open" class="json-punct">{{ array ? "]" : "}" }}</span>
    </button>

    <template v-if="container && open">
      <JsonNode
        v-for="entry in entries"
        :key="entry.key"
        :name="entry.key"
        :value="entry.value"
        :index="entry.index"
        :depth="depth + 1"
      />
      <div class="json-close-line" :style="closeStyle">{{ array ? "]" : "}" }}</div>
    </template>

    <template v-if="!container">
      <div class="json-line" :style="lineStyle">
        <span class="json-twisty" aria-hidden="true"></span>
        <span v-if="hasName" class="json-label" :class="{ 'json-index': index }">{{ label }}</span><span v-if="hasName" class="json-colon">:</span>
        <span class="json-value" :class="`json-${kind}`">{{ preview }}</span>
        <button
          v-if="longString"
          type="button"
          class="link-btn json-long-toggle"
          @click="expandedString = !expandedString"
        >{{ expandedString ? "收起" : "展开全文" }}</button>
      </div>
      <pre v-if="longString && expandedString" class="json-full-string" :style="fullStyle">{{ fullString }}</pre>
    </template>
  </div>
</template>
