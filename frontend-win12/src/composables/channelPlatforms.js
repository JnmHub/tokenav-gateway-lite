export function makeChannelSection(platform) {
  return {
    platform, enabled: true, group_ids: [], model_mapping: {}, model_pricing: [],
    web_search_emulation: false, codex_image_generation_bridge: false, bedrock_cc_compat: false,
  }
}

export function setChannelPlatform(platforms, platform, enabled) {
  let section = platforms.find((item) => item.platform === platform)
  if (!section) {
    section = makeChannelSection(platform)
    platforms.push(section)
  }
  // 根据复选框目标状态赋值，不对工厂默认 true 再取反；首次点击必须真正启用。
  section.enabled = enabled
  return section
}
