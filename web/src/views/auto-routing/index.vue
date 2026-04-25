<template>
  <div class="auto-routing">
    <div class="header">
      <h2>Auto 路由配置</h2>
      <el-switch
        v-model="config.enabled"
        active-text="启用"
        inactive-text="禁用"
        @change="handleSave"
      />
    </div>

    <el-card class="threshold-card">
      <template #header>
        <span>复杂度阈值</span>
      </template>
      <el-form label-width="120px">
        <el-form-item label="Simple 阈值">
          <el-input-number
            v-model="config.simple_threshold"
            :min="0"
            :step="100"
            @change="handleSave"
          />
          <span class="hint">token &lt; 此值视为简单请求</span>
        </el-form-item>
        <el-form-item label="Complex 阈值">
          <el-input-number
            v-model="config.complex_threshold"
            :min="0"
            :step="100"
            @change="handleSave"
          />
          <span class="hint">token &gt; 此值视为复杂请求</span>
        </el-form-item>
      </el-form>
    </el-card>

    <el-card class="mapping-card">
      <template #header>
        <div class="card-header">
          <span>模型映射配置</span>
          <el-button type="primary" size="small" @click="addMapping">
            添加映射
          </el-button>
        </div>
      </template>

      <el-table :data="mappingList" border stripe>
        <el-table-column prop="model" label="逻辑模型" width="150">
          <template #default="{ row }">
            <el-input v-model="row.model" placeholder="如 gpt-4o" />
          </template>
        </el-table-column>
        <el-table-column prop="simple" label="Simple 分组" width="180">
          <template #default="{ row }">
            <el-input v-model="row.simple" placeholder="lite 分组" />
          </template>
        </el-table-column>
        <el-table-column prop="medium" label="Medium 分组" width="180">
          <template #default="{ row }">
            <el-input v-model="row.medium" placeholder="pro 分组" />
          </template>
        </el-table-column>
        <el-table-column prop="complex" label="Complex 分组" width="180">
          <template #default="{ row }">
            <el-input v-model="row.complex" placeholder="max 分组" />
          </template>
        </el-table-column>
        <el-table-column label="操作" width="100">
          <template #default="{ row, $index }">
            <el-button type="danger" size="small" @click="removeMapping($index)">
              删除
            </el-button>
          </template>
        </el-table-column>
      </el-table>
    </el-card>

    <el-card class="test-card">
      <template #header>
        <span>路由测试</span>
      </template>
      <el-form label-width="100px">
        <el-form-item label="分组名">
          <el-input v-model="testForm.groupName" placeholder="如 gpt-4o" />
        </el-form-item>
        <el-form-item label="请求体">
          <el-input
            v-model="testForm.requestBody"
            type="textarea"
            :rows="6"
            placeholder='{"messages":[{"role":"user","content":"hello"}]}'
          />
        </el-form-item>
        <el-form-item>
          <el-button type="primary" @click="handleTest" :loading="testing">
            测试路由
          </el-button>
        </el-form-item>
      </el-form>

      <div v-if="testResult" class="test-result">
        <el-divider>测试结果</el-divider>
        <el-descriptions :column="2" border>
          <el-descriptions-item label="目标分组">
            {{ testResult.target_group }}
          </el-descriptions-item>
          <el-descriptions-item label="复杂度级别">
            <el-tag :type="getLevelType(testResult.analysis?.level)">
              {{ testResult.analysis?.level }}
            </el-tag>
          </el-descriptions-item>
          <el-descriptions-item label="估算 Token">
            {{ testResult.analysis?.estimated_tokens }}
          </el-descriptions-item>
          <el-descriptions-item label="包含 Tools">
            {{ testResult.analysis?.has_tools ? '是' : '否' }}
          </el-descriptions-item>
          <el-descriptions-item label="包含 Vision">
            {{ testResult.analysis?.has_vision ? '是' : '否' }}
          </el-descriptions-item>
          <el-descriptions-item label="使用回退">
            {{ testResult.fallback_used ? '是' : '否' }}
          </el-descriptions-item>
        </el-descriptions>
      </div>
    </el-card>
  </div>
</template>

<script setup lang="ts">
import { ref, reactive, onMounted } from 'vue';
import { ElMessage } from 'element-plus';
import {
  autoRoutingApi,
  type RouteConfig,
  type TestRouteResponse,
} from '../api/auto-routing';

interface MappingRow {
  model: string;
  simple: string;
  medium: string;
  complex: string;
}

const config = ref<RouteConfig>({
  enabled: false,
  simple_threshold: 2000,
  complex_threshold: 8000,
  group_mapping: {},
});

const mappingList = ref<MappingRow[]>([]);
const testForm = reactive({
  groupName: 'gpt-4o',
  requestBody: '{"messages":[{"role":"user","content":"hello"}]}',
});
const testResult = ref<TestRouteResponse | null>(null);
const testing = ref(false);

function configToList(cfg: RouteConfig): MappingRow[] {
  return Object.entries(cfg.group_mapping).map(([model, mapping]) => ({
    model,
    simple: mapping.simple_group,
    medium: mapping.medium_group,
    complex: mapping.complex_group,
  }));
}

function listToConfig(list: MappingRow[]): Record<string, any> {
  const mapping: Record<string, any> = {};
  for (const row of list) {
    if (row.model) {
      mapping[row.model] = {
        simple_group: row.simple,
        medium_group: row.medium,
        complex_group: row.complex,
      };
    }
  }
  return mapping;
}

async function loadConfig() {
  try {
    const resp = await autoRoutingApi.getConfig();
    if (resp.success && resp.config) {
      config.value = resp.config;
      mappingList.value = configToList(resp.config);
    }
  } catch (e) {
    ElMessage.error('加载配置失败');
  }
}

async function handleSave() {
  config.value.group_mapping = listToConfig(mappingList.value);
  try {
    const resp = await autoRoutingApi.saveConfig({
      enabled: config.value.enabled,
      simple_threshold: config.value.simple_threshold,
      complex_threshold: config.value.complex_threshold,
      group_mapping: config.value.group_mapping,
    });
    if (resp.success) {
      ElMessage.success('保存成功');
    } else {
      ElMessage.error('保存失败: ' + resp.error);
    }
  } catch (e) {
    ElMessage.error('保存失败');
  }
}

function addMapping() {
  mappingList.value.push({
    model: '',
    simple: '',
    medium: '',
    complex: '',
  });
}

function removeMapping(index: number) {
  mappingList.value.splice(index, 1);
  handleSave();
}

async function handleTest() {
  testing.value = true;
  testResult.value = null;
  try {
    const requestBody = JSON.parse(testForm.requestBody);
    const resp = await autoRoutingApi.testRoute({
      group_name: testForm.groupName,
      request_body: requestBody,
    });
    testResult.value = resp;
    if (!resp.success) {
      ElMessage.error('测试失败: ' + resp.error);
    }
  } catch (e: any) {
    ElMessage.error('请求体格式错误: ' + e.message);
  } finally {
    testing.value = false;
  }
}

function getLevelType(level?: string): string {
  switch (level) {
    case 'simple':
      return 'success';
    case 'medium':
      return 'warning';
    case 'complex':
      return 'danger';
    default:
      return 'info';
  }
}

onMounted(() => {
  loadConfig();
});
</script>

<style scoped>
.auto-routing {
  padding: 20px;
}

.header {
  display: flex;
  justify-content: space-between;
  align-items: center;
  margin-bottom: 20px;
}

.header h2 {
  margin: 0;
}

.threshold-card,
.mapping-card,
.test-card {
  margin-bottom: 20px;
}

.card-header {
  display: flex;
  justify-content: space-between;
  align-items: center;
}

.hint {
  margin-left: 10px;
  color: #999;
  font-size: 12px;
}

.test-result {
  margin-top: 20px;
}
</style>
